package plugin

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
)

// SDKVersion is published in service metadata so core can reason about which
// plugins predate a change to this contract.
const SDKVersion = "0.1.0"

// Metadata keys published on the NATS service registry. Everything here is
// visible to core and therefore a candidate for model context, so nothing
// sensitive may be placed in service or endpoint metadata.
const (
	MetaName         = "azir.name"
	MetaDescription  = "azir.description"
	MetaSummary      = "azir.summary"
	MetaProvides     = "azir.provides"
	MetaMutates      = "azir.mutates"
	MetaSchema       = "azir.schema"
	MetaFreshness    = "azir.freshness"
	MetaPermission   = "azir.permission"
	MetaCategory     = "azir.category"
	MetaConfigScope  = "azir.config_scope"
	MetaConfigSchema = "azir.config_schema"
	MetaSDK          = "azir.sdk"
)

// SubjectPrefix is the root of the tool request/reply namespace.
const SubjectPrefix = "azir.tool"

// endpointName renders a tool name as a NATS micro endpoint name.
//
// micro validates endpoint names as a single token and rejects dots, but dots
// are exactly what Azir's tool names use — "tickets.search" reads far better
// to a model than "tickets_search", and the capability vocabulary is dotted
// for the same reason. So the endpoint name is sanitised while the subject
// keeps its dots, and the real name travels in metadata for discovery.
func endpointName(tool string) string {
	return strings.ReplaceAll(tool, ".", "_")
}

// toolSubject is the full request/reply subject for a tool.
func toolSubject(pluginName, tool string) string {
	return SubjectPrefix + "." + pluginName + "." + tool
}

type options struct {
	natsURL string
	logger  *slog.Logger
	secrets []string
}

// Option configures Serve.
type Option func(*options)

// WithNATSURL overrides the server URL. Defaults to $NATS_URL, then
// nats://localhost:4222.
func WithNATSURL(url string) Option { return func(o *options) { o.natsURL = url } }

// WithLogger sets the logger. The SDK never logs credential material, but a
// caller-supplied logger should still install a redacting handler.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithSecrets registers literal secret values for outbound redaction. Pass
// every credential resolved for this plugin, so a value cannot escape by being
// embedded somewhere the key-name rules would miss.
func WithSecrets(values ...string) Option {
	return func(o *options) { o.secrets = append(o.secrets, values...) }
}

// Serve registers the plugin and blocks until ctx is cancelled.
//
// This function is the entire transport boundary. It currently registers a
// NATS micro service; replacing that with another transport would not change
// any plugin's code.
//
// A tool that declares Mutates is registered like any other, having been
// refused entirely when Azir was read-only. What replaced the refusal is
// narrower and lives in validate: a mutating tool must name the permission it
// requires, or the plugin does not start. A write nobody has to be allowed to
// make is not a write anyone should make.
func Serve(ctx context.Context, p Plugin, opts ...Option) error {
	o := options{
		natsURL: envOr("NATS_URL", nats.DefaultURL),
		logger:  slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	for _, fn := range opts {
		fn(&o)
	}
	log := o.logger.With("plugin", p.Name, "version", p.Version)

	if err := p.validate(); err != nil {
		return err
	}

	nc, err := nats.Connect(o.natsURL,
		nats.Name("azir-plugin-"+p.Name),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Warn("nats disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			log.Info("nats reconnected", "url", c.ConnectedUrl())
		}),
	)
	if err != nil {
		return err
	}
	defer nc.Drain() //nolint:errcheck // best effort on shutdown

	red := NewRedactor(o.secrets...)

	// Credentials and settings are resolved from core at request time rather
	// than injected as environment variables, so an administrator can change
	// them in the web console without redeploying anything. Both live outside
	// azir.tool.*, so neither is ever discovered as a model-facing capability.
	vault := newVault(nc, p.Name, red, func(secret string) {
		// A credential learned at runtime must also be scrubbed from this
		// plugin's log output, not only from its responses.
		if h, ok := o.logger.Handler().(interface{ Register(...string) }); ok {
			h.Register(secret)
		}
	})
	cfg := newConfig(nc, p.Name)
	ident := newIdentity(nc, p.Name)

	svc, err := micro.AddService(nc, micro.Config{
		Name:        p.Name,
		Version:     p.Version,
		Description: p.Description,
		Metadata:    serviceMetadata(p),
		ErrorHandler: func(_ micro.Service, err *micro.NATSError) {
			log.Error("service error", "subject", err.Subject, "error", err.Description)
		},
	})
	if err != nil {
		return err
	}
	defer svc.Stop() //nolint:errcheck // best effort on shutdown

	// Health is served on its own subject so core can ask what still works
	// without waiting for a tool call to fail.
	// Preflight needs the same context handlers get: it typically has to read
	// settings and resolve a credential to decide what works. Without this it
	// can only ever report "not configured".
	pluginCtx := withIdentity(withConfig(withVault(context.Background(), vault), cfg), ident)
	health := newHealthCache(p.Preflight)
	healthSub, err := serveHealth(nc, p.Name, health, pluginCtx)
	if err != nil {
		return err
	}
	defer healthSub.Unsubscribe() //nolint:errcheck // best effort on shutdown

	// An administrator pressing save takes effect now rather than whenever the
	// caches happen to expire. Health is dropped too: what a plugin can do is
	// mostly a function of what it has been configured with, so a stale answer
	// there would report the old capability set just as misleadingly.
	changedSub, err := nc.Subscribe(ConfigChangedSubject(p.Name), func(*nats.Msg) {
		cfg.Invalidate()
		vault.Forget("", "")
		vault.ForgetAll()
		health.invalidate()
		log.Info("settings changed; caches dropped")
	})
	if err != nil {
		return err
	}
	defer changedSub.Unsubscribe() //nolint:errcheck // best effort on shutdown

	for _, t := range p.Tools {
		if err := svc.AddEndpoint(
			endpointName(t.Name),
			micro.HandlerFunc(wrap(t, red, vault, cfg, ident, log)),
			micro.WithEndpointSubject(toolSubject(p.Name, t.Name)),
			micro.WithEndpointMetadata(toolMetadata(t)),
		); err != nil {
			return fmt.Errorf("register tool %q: %w", t.Name, err)
		}
		log.Info("registered tool",
			"tool", t.Name,
			"subject", toolSubject(p.Name, t.Name),
			"provides", capsToStrings(t.Provides))
	}

	log.Info("plugin ready", "url", nc.ConnectedUrl(), "tools", len(p.Tools))
	<-ctx.Done()
	log.Info("shutting down")
	return nil
}

func toolMetadata(t Tool) map[string]string {
	meta := map[string]string{
		// The true name, since the endpoint name has had its dots removed.
		MetaName:        t.Name,
		MetaDescription: t.Description,
		// Falls back to the model-facing text, so a plugin that has not written
		// a human summary still says something rather than nothing.
		MetaSummary:  cmp.Or(t.Summary, t.Description),
		MetaProvides: strings.Join(capsToStrings(t.Provides), ","),
		MetaMutates:  strconv.FormatBool(t.Mutates),
		MetaSchema:   string(stripSecretFields(t.Schema, t.Secrets)),
	}
	if t.Freshness != nil {
		meta[MetaFreshness] = t.Freshness.String()
	}
	if t.RequiresPermission != "" {
		meta[MetaPermission] = t.RequiresPermission
	}
	return meta
}

// serviceMetadata publishes what an administrator needs to configure this
// plugin. The config schema travels with the plugin so the console can render
// its settings form generically — adding a plugin requires no frontend work,
// which is the difference between modular and merely decoupled.
func serviceMetadata(p Plugin) map[string]string {
	return map[string]string{
		MetaCategory:     string(categoryOrOther(p.Category)),
		MetaConfigScope:  string(p.ConfigScope),
		MetaSDK:          SDKVersion,
		MetaConfigSchema: string(p.ConfigSchema),
	}
}

// wrap adapts a Handler to the transport, and is where the SDK's two
// non-negotiable behaviours live: every return value is redacted, and no
// unrecognised error is ever echoed back to the caller.
func wrap(t Tool, red *Redactor, vault *Vault, cfg *Config, ident *Identity, log *slog.Logger) func(micro.Request) {
	return func(r micro.Request) {
		var req Request
		if len(r.Data()) > 0 {
			if err := json.Unmarshal(r.Data(), &req); err != nil {
				_ = r.Error("400", "invalid request envelope", nil)
				return
			}
		}

		ctx := withConfig(withVault(context.Background(), vault), cfg)
		out, err := t.Handler(ctx, req)
		if err != nil {
			var perr *Error
			if errors.As(err, &perr) {
				// A controlled error is documented as caller-safe, but
				// documentation is not enforcement: an author formatting a
				// credential into Errorf would otherwise leak it straight to
				// the caller, and from there into model context.
				_ = r.Error(perr.Code, red.Text(perr.Message), nil)
				return
			}
			// Unrecognised errors routinely carry request URLs and auth
			// headers. Log the detail locally; tell the caller nothing.
			log.Error("tool failed",
				"tool", t.Name, "customer_id", req.CustomerID, "error", err)
			_ = r.Error("500", "internal plugin error", nil)
			return
		}

		clean, err := red.Value(out)
		if err != nil {
			log.Error("redaction failed", "tool", t.Name, "error", err)
			_ = r.Error("500", "internal plugin error", nil)
			return
		}
		if err := r.RespondJSON(clean); err != nil {
			log.Error("respond failed", "tool", t.Name, "error", err)
		}
	}
}

func capsToStrings(caps []Capability) []string {
	out := make([]string, len(caps))
	for i, c := range caps {
		out[i] = string(c)
	}
	return out
}

func categoryOrOther(c Category) Category {
	if c == "" {
		return CategoryOther
	}
	return c
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
