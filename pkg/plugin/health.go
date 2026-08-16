package plugin

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// HealthSubjectPrefix is where a plugin reports which of its tools can
// currently do their job. Per-plugin, and outside azir.tool.*, like the other
// core-facing subjects.
const HealthSubjectPrefix = "azir.health"

// HealthSubject returns the health subject for a plugin.
func HealthSubject(pluginName string) string {
	return HealthSubjectPrefix + "." + pluginName
}

// ToolStatus reports whether one tool can work right now.
//
// This exists because partial capability is the normal case, not an edge one.
// An administrator granting Azir "view tickets" and nothing else is being
// sensible, and the correct response is for the ticket tools to work and the
// invoice tools to say why they cannot — not for the plugin to fail, and not
// for a technician to discover it as an opaque 401 mid-conversation.
type ToolStatus struct {
	Available bool `json:"available"`
	// Reason is shown to an administrator, so it should name what is missing
	// and where to fix it.
	Reason string `json:"reason,omitempty"`
}

// Health is a plugin's self-assessment.
type Health struct {
	// Ready is false when the plugin cannot work at all — unconfigured, or
	// unable to reach its vendor. Individual tools may still be listed.
	Ready bool `json:"ready"`
	// Reason explains a not-ready plugin.
	Reason string `json:"reason,omitempty"`
	// Tools maps tool name to status. A tool absent from this map is assumed
	// available: silence should not disable working functionality.
	Tools map[string]ToolStatus `json:"tools,omitempty"`
	// CheckedAt is when the plugin last determined this.
	CheckedAt time.Time `json:"checked_at"`
}

// Preflight determines which tools can currently work.
//
// A plugin implements this however it likes — introspecting a token's
// permissions, pinging a vendor, checking whether it is configured at all.
// Returning nil means everything is assumed available, which is the right
// default for plugins with nothing to check.
type Preflight func(ctx context.Context) Health

// healthCache serves preflight results without re-running an expensive check
// on every query. Core polls discovery every ten seconds, and asking a vendor
// for its permission matrix that often would be wasteful and rude.
type healthCache struct {
	fn  Preflight
	ttl time.Duration

	mu      sync.Mutex
	last    Health
	lastAt  time.Time
	hasLast bool
}

func newHealthCache(fn Preflight) *healthCache {
	return &healthCache{fn: fn, ttl: 60 * time.Second}
}

func (h *healthCache) get(ctx context.Context) Health {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.hasLast && time.Since(h.lastAt) < h.ttl {
		return h.last
	}
	if h.fn == nil {
		// No preflight: ready, with nothing to qualify.
		h.last = Health{Ready: true, CheckedAt: time.Now().UTC()}
	} else {
		result := h.fn(ctx)
		result.CheckedAt = time.Now().UTC()
		h.last = result
	}
	h.lastAt = time.Now()
	h.hasLast = true
	return h.last
}

// serveHealth answers health queries for a plugin.
//
// base carries the vault, settings and identity resolvers, because a preflight
// almost always needs them: deciding what works usually means reading a
// setting and asking a vendor what a credential is allowed to do.
func serveHealth(nc *nats.Conn, pluginName string, cache *healthCache, base context.Context) (*nats.Subscription, error) {
	return nc.Subscribe(HealthSubject(pluginName), func(m *nats.Msg) {
		ctx, cancel := context.WithTimeout(base, 10*time.Second)
		defer cancel()

		body, err := json.Marshal(cache.get(ctx))
		if err != nil {
			return
		}
		_ = m.Respond(body)
	})
}
