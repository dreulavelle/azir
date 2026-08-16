package plugin

import (
	"context"
	"encoding/json"
	"errors"
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
	MetaDescription  = "azir.description"
	MetaProvides     = "azir.provides"
	MetaMutates      = "azir.mutates"
	MetaSchema       = "azir.schema"
	MetaCategory     = "azir.category"
	MetaConfigSchema = "azir.config_schema"
	MetaSDK          = "azir.sdk"
)

// SubjectPrefix is the root of the tool request/reply namespace.
const SubjectPrefix = "azir.tool"

type options struct {
	natsURL string
	logger  *slog.Logger
	secrets []string
	name    string
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
// Serve returns [ErrMutatingTool] before opening a connection if any tool
// declares Mutates, so a write-capable plugin fails to boot rather than
// failing quietly at request time.
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

	svc, err := micro.AddService(nc, micro.Config{
		Name:        p.Name,
		Version:     p.Version,
		Description: p.Description,
		Metadata: map[string]string{
			MetaCategory:     string(categoryOrOther(p.Category)),
			MetaSDK:          SDKVersion,
			MetaConfigSchema: string(p.ConfigSchema),
		},
		ErrorHandler: func(_ micro.Service, err *micro.NATSError) {
			log.Error("service error", "subject", err.Subject, "error", err.Description)
		},
	})
	if err != nil {
		return err
	}
	defer svc.Stop() //nolint:errcheck // best effort on shutdown

	group := svc.AddGroup(SubjectPrefix + "." + p.Name)
	for _, t := range p.Tools {
		if err := group.AddEndpoint(
			t.Name,
			micro.HandlerFunc(wrap(t, red, log)),
			micro.WithEndpointMetadata(toolMetadata(t)),
		); err != nil {
			return err
		}
		log.Info("registered tool",
			"tool", t.Name,
			"subject", SubjectPrefix+"."+p.Name+"."+t.Name,
			"provides", capsToStrings(t.Provides))
	}

	log.Info("plugin ready", "url", nc.ConnectedUrl(), "tools", len(p.Tools))
	<-ctx.Done()
	log.Info("shutting down")
	return nil
}

func toolMetadata(t Tool) map[string]string {
	return map[string]string{
		MetaDescription: t.Description,
		MetaProvides:    strings.Join(capsToStrings(t.Provides), ","),
		MetaMutates:     strconv.FormatBool(t.Mutates),
		MetaSchema:      string(stripSecretFields(t.Schema, t.Secrets)),
	}
}

// wrap adapts a Handler to the transport, and is where the SDK's two
// non-negotiable behaviours live: every return value is redacted, and no
// unrecognised error is ever echoed back to the caller.
func wrap(t Tool, red *Redactor, log *slog.Logger) func(micro.Request) {
	return func(r micro.Request) {
		var req Request
		if len(r.Data()) > 0 {
			if err := json.Unmarshal(r.Data(), &req); err != nil {
				_ = r.Error("400", "invalid request envelope", nil)
				return
			}
		}

		out, err := t.Handler(context.Background(), req)
		if err != nil {
			var perr *Error
			if errors.As(err, &perr) {
				_ = r.Error(perr.Code, perr.Message, nil)
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
