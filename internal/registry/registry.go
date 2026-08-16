// Package registry discovers plugins from the NATS service registry and keeps
// core's view of what the deployment can currently do.
//
// Discovery proposes; it does not grant. A tool appearing in $SRV becomes a
// candidate capability. Phase 1 adds the administrator approval gate that
// decides which candidates the model may actually use — without it, anyone
// able to start a container could extend what Azir can do.
package registry

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"

	"github.com/dreulavelle/azir/pkg/plugin"
)

// srvInfoSubject is the NATS service-discovery subject. Every micro service
// replies to it with its identity and endpoints.
const srvInfoSubject = "$SRV.INFO"

// Tool is a discovered endpoint.
type Tool struct {
	Plugin      string              `json:"plugin"`
	Name        string              `json:"name"`
	Subject     string              `json:"subject"`
	Description string              `json:"description"`
	Provides    []plugin.Capability `json:"provides"`
	Mutates     bool                `json:"mutates"`
	Schema      json.RawMessage     `json:"schema,omitempty"`

	// Available reports whether the plugin says this tool can currently do its
	// job. Partial capability is normal — a token granted only ticket access
	// is a sensible thing for an administrator to issue — so an unusable tool
	// explains itself rather than failing at call time.
	Available bool   `json:"available"`
	Reason    string `json:"unavailable_reason,omitempty"`

	// Freshness is the staleness budget the plugin declared. Nil means results
	// are never cached.
	Freshness *plugin.Freshness `json:"freshness,omitempty"`
}

// Plugin is a discovered service.
type Plugin struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	ID          string          `json:"id"`
	Description string          `json:"description"`
	Category    plugin.Category `json:"category"`
	SDK         string          `json:"sdk"`
	// Ready is false when the plugin cannot work at all, with Reason saying why.
	Ready  bool   `json:"ready"`
	Reason string `json:"not_ready_reason,omitempty"`
	// ConfigSchema is the JSON Schema for this plugin's settings, published by
	// the plugin itself. The admin console renders its form from this, so
	// adding a plugin requires no frontend work.
	ConfigSchema json.RawMessage `json:"config_schema,omitempty"`
	Tools        []Tool          `json:"tools"`
}

// Snapshot is an immutable view of the registry at one moment.
type Snapshot struct {
	Plugins    []Plugin            `json:"plugins"`
	Capability map[string][]string `json:"capabilities"`
	At         time.Time           `json:"at"`
}

// Observer is notified of every tool seen in discovery, so the approval gate
// can record it as a candidate. Discovery proposes; it never grants.
type Observer func(ctx context.Context, plugin, tool string, provides []string) error

// Registry polls service discovery and caches the result.
type Registry struct {
	nc     *nats.Conn
	log    *slog.Logger
	window time.Duration

	mu       sync.RWMutex
	snap     Snapshot
	observer Observer
}

// SetObserver installs a callback invoked for each discovered tool.
func (r *Registry) SetObserver(o Observer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observer = o
}

// New returns a Registry. The window is how long discovery waits to collect
// replies; services answer immediately, so a few hundred milliseconds is
// ample.
func New(nc *nats.Conn, log *slog.Logger, window time.Duration) *Registry {
	if window <= 0 {
		window = 500 * time.Millisecond
	}
	return &Registry{
		nc:     nc,
		log:    log,
		window: window,
		snap:   Snapshot{Capability: map[string][]string{}},
	}
}

// Run refreshes the registry on every tick until ctx is cancelled. It performs
// one refresh immediately so core is useful before the first tick.
func (r *Registry) Run(ctx context.Context, every time.Duration) {
	if err := r.Refresh(ctx); err != nil {
		r.log.Warn("initial discovery failed", "error", err)
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.Refresh(ctx); err != nil {
				r.log.Warn("discovery failed", "error", err)
			}
		}
	}
}

// Refresh performs one discovery sweep and replaces the snapshot.
func (r *Registry) Refresh(ctx context.Context) error {
	infos, err := r.discover(ctx)
	if err != nil {
		return err
	}

	plugins := make([]Plugin, 0, len(infos))
	caps := map[string][]string{}

	for _, info := range infos {
		p := Plugin{
			Name:        info.Name,
			Version:     info.Version,
			ID:          info.ID,
			Description: info.Description,
			Category:    plugin.Category(info.Metadata[plugin.MetaCategory]),
			SDK:         info.Metadata[plugin.MetaSDK],
		}
		if schema := info.Metadata[plugin.MetaConfigSchema]; schema != "" {
			p.ConfigSchema = json.RawMessage(schema)
		}

		// Ask the plugin what still works. A plugin that does not answer is
		// treated as fully available: silence must not disable functionality.
		health := r.health(ctx, info.Name)
		p.Ready, p.Reason = health.Ready, health.Reason
		for _, ep := range info.Endpoints {
			// The endpoint name has dots stripped for micro's validator; the
			// real tool name travels in metadata.
			name := ep.Metadata[plugin.MetaName]
			if name == "" {
				name = ep.Name
			}
			t := Tool{
				Plugin:      info.Name,
				Name:        name,
				Subject:     ep.Subject,
				Description: ep.Metadata[plugin.MetaDescription],
				Mutates:     ep.Metadata[plugin.MetaMutates] == "true",
				Provides:    parseCaps(ep.Metadata[plugin.MetaProvides]),
			}
			if s := ep.Metadata[plugin.MetaSchema]; s != "" {
				t.Schema = json.RawMessage(s)
			}
			// Absent freshness means never cache, which is the right default
			// for anything whose answer is expected to be live.
			if f, ok := plugin.ParseFreshness(ep.Metadata[plugin.MetaFreshness]); ok {
				t.Freshness = &f
			}
			// A mutating tool should be impossible: the SDK refuses to start
			// with one. If a non-SDK service ever advertises one, core must
			// not treat it as usable.
			if t.Mutates {
				r.log.Error("refusing mutating tool from discovery",
					"plugin", info.Name, "tool", ep.Name, "subject", ep.Subject)
				continue
			}

			// A tool the plugin has not reported on is assumed available:
			// silence must not disable working functionality.
			t.Available = true
			if status, reported := health.Tools[t.Name]; reported {
				t.Available, t.Reason = status.Available, status.Reason
			}

			// An unavailable tool provides nothing. Leaving it in the
			// capability index would let a feature believe it is supported and
			// then fail when it calls.
			if !t.Available {
				p.Tools = append(p.Tools, t)
				continue
			}

			for _, c := range t.Provides {
				key := string(c)
				// t.Name, not ep.Name: the endpoint name has had its dots
				// stripped for micro's validator, while the approval gate keys
				// on the real name. Using the sanitised one here silently
				// produces provider keys that nothing else matches.
				caps[key] = append(caps[key], info.Name+"."+t.Name)
			}
			p.Tools = append(p.Tools, t)
		}
		slices.SortFunc(p.Tools, func(a, b Tool) int { return strings.Compare(a.Name, b.Name) })
		plugins = append(plugins, p)
	}

	slices.SortFunc(plugins, func(a, b Plugin) int { return strings.Compare(a.Name, b.Name) })
	for k := range caps {
		slices.Sort(caps[k])
	}

	r.mu.Lock()
	r.snap = Snapshot{Plugins: plugins, Capability: caps, At: time.Now().UTC()}
	observer := r.observer
	r.mu.Unlock()

	// Recording candidates must not fail discovery: core stays useful with a
	// stale approval view, but not with no registry at all.
	if observer != nil {
		for _, p := range plugins {
			for _, t := range p.Tools {
				provides := make([]string, len(t.Provides))
				for i, c := range t.Provides {
					provides[i] = string(c)
				}
				if err := observer(ctx, p.Name, t.Name, provides); err != nil {
					r.log.Warn("could not record discovered tool",
						"plugin", p.Name, "tool", t.Name, "error", err)
				}
			}
		}
	}
	return nil
}

// Snapshot returns the current view.
func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snap
}

// Lookup finds a discovered tool by plugin and tool name.
func (r *Registry) Lookup(pluginName, toolName string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.snap.Plugins {
		if p.Name != pluginName {
			continue
		}
		for _, t := range p.Tools {
			if t.Name == toolName {
				return t, true
			}
		}
	}
	return Tool{}, false
}

// Providers returns the tools satisfying a capability, as "plugin.tool".
// Features use this to report themselves unavailable with a reason rather than
// failing opaquely.
func (r *Registry) Providers(c plugin.Capability) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.snap.Capability[string(c)])
}

// discover publishes a single $SRV.INFO request and collects replies for the
// discovery window. There is no request-many primitive in the client, so the
// inbox is managed directly.
func (r *Registry) discover(ctx context.Context) ([]micro.Info, error) {
	inbox := nats.NewInbox()
	sub, err := r.nc.SubscribeSync(inbox)
	if err != nil {
		return nil, err
	}
	defer sub.Unsubscribe() //nolint:errcheck // best effort

	if err := r.nc.PublishRequest(srvInfoSubject, inbox, nil); err != nil {
		return nil, err
	}
	if err := r.nc.Flush(); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(r.window)
	var out []micro.Info
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		msg, err := sub.NextMsg(remaining)
		if err != nil {
			break // timeout ends the collection window
		}
		var info micro.Info
		if err := json.Unmarshal(msg.Data, &info); err != nil {
			r.log.Warn("undecodable service info", "error", err)
			continue
		}
		out = append(out, info)

		if ctx.Err() != nil {
			return out, ctx.Err()
		}
	}
	return out, nil
}

// health asks a plugin which of its tools can currently work. A plugin that
// does not answer is assumed fully available, because a missing reply is far
// more likely to mean "no preflight implemented" than "everything is broken".
func (r *Registry) health(ctx context.Context, pluginName string) plugin.Health {
	assumed := plugin.Health{Ready: true}

	msg, err := r.nc.RequestWithContext(ctx, plugin.HealthSubject(pluginName), nil)
	if err != nil {
		return assumed
	}
	var h plugin.Health
	if err := json.Unmarshal(msg.Data, &h); err != nil {
		r.log.Warn("undecodable plugin health", "plugin", pluginName, "error", err)
		return assumed
	}
	return h
}

func parseCaps(csv string) []plugin.Capability {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]plugin.Capability, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, plugin.Capability(p))
		}
	}
	return out
}
