// Package plugin is the SDK for building Azir plugins.
//
// A plugin author writes capabilities and handlers. Nothing in this package's
// surface mentions NATS: [Serve] is the entire transport boundary, so the
// transport can be replaced without touching plugin code.
//
// Two responsibilities live here rather than in each plugin, because
// enforcement that is opt-in per plugin is not enforcement:
//
//   - Outbound redaction of every handler return value ([Redactor]).
//   - Refusal to expose any mutating tool at all ([Serve] returns an error).
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Handler executes one tool invocation. Returning a [*Error] produces a
// controlled error response; any other error is logged locally and reported to
// the caller as a generic failure, so a wrapped vendor error can never carry a
// URL, header or credential into model context.
type Handler func(ctx context.Context, req Request) (any, error)

// Request is what a handler receives. CustomerID is an opaque handle into
// Azir's customer spine — never an FQDN, an account number, or anything from
// which a credential could be derived. The plugin resolves credentials for
// that customer server-side; the model never sees them.
type Request struct {
	CustomerID string          `json:"customer_id"`
	Actor      Actor           `json:"actor"`
	Args       json.RawMessage `json:"args,omitempty"`
}

// Actor identifies who caused the invocation, for audit and for the RBAC
// re-check at the tool boundary. Authorization is never trusted from the
// client.
type Actor struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// Tool is a single capability a plugin exposes.
type Tool struct {
	// Name is the endpoint name, e.g. "tickets.search". It is appended to the
	// plugin's subject group to form azir.tool.<plugin>.<name>.
	Name string

	// Description is surfaced to the model. Write it for a reader who knows
	// the domain but not this vendor's API.
	Description string

	// Schema is a JSON Schema for Args. Properties named in Secrets are
	// stripped before publication, so the model cannot request them.
	Schema json.RawMessage

	// Provides lists the capability tags this tool satisfies. Features declare
	// the capabilities they require and report themselves unavailable, with a
	// reason, when nothing supplies them.
	Provides []Capability

	// Mutates marks a tool that writes to an external system.
	//
	// A mutating tool is never offered to the model. Not gated, not
	// approval-wrapped — absent from the tool list entirely, because the whole
	// prompt-injection containment argument rests on there being no action for
	// an injection to reach. Ticket bodies are attacker-controlled text, and a
	// model that cannot act cannot be talked into acting.
	//
	// Mutating tools are invoked only by an authenticated person, holding the
	// permission the tool declares, in a deployment where an administrator has
	// enabled writes for that plugin. Three independent conditions, because
	// this is the one place where being wrong is irreversible.
	Mutates bool

	// RequiresPermission names the permission a caller must hold. Meaningful
	// only for mutating tools; reads are governed by tool.read.
	RequiresPermission string

	// Secrets names Schema properties that must never reach the model.
	Secrets []string

	// Freshness declares how long a result may be reused. Nil means never
	// cache, which is the right default for anything whose answer is expected
	// to be live.
	//
	// The plugin declares it because only the plugin knows how volatile its
	// data is: a ticket status changes while you are reading it, a customer's
	// phone number does not. Core implements it, because caching is generic
	// and every plugin would otherwise reinvent it slightly differently.
	Freshness *Freshness

	Handler Handler
}

// Freshness is a two-level staleness budget.
//
// Below Soft a cached result is served immediately. Between Soft and Hard it is
// still served immediately, and a refresh runs behind it — the caller gets a
// fast answer and the next one gets a current answer. Beyond Hard the caller
// waits for fresh data.
//
// Serving something slightly stale is fine. Serving it while implying it is
// live is not, so every cached response carries the age of the data.
type Freshness struct {
	Soft time.Duration
	Hard time.Duration
}

// String renders a freshness budget for service metadata.
func (f Freshness) String() string {
	return f.Soft.String() + "/" + f.Hard.String()
}

// ParseFreshness reads what String wrote.
func ParseFreshness(s string) (Freshness, bool) {
	soft, hard, ok := strings.Cut(s, "/")
	if !ok {
		return Freshness{}, false
	}
	sd, err1 := time.ParseDuration(soft)
	hd, err2 := time.ParseDuration(hard)
	if err1 != nil || err2 != nil {
		return Freshness{}, false
	}
	return Freshness{Soft: sd, Hard: hd}, true
}

// Plugin describes a whole service.
type Plugin struct {
	Name        string
	Version     string
	Description string
	Category    Category

	// ConfigSchema is a JSON Schema for this plugin's connection settings —
	// Syncro needs a subdomain and API key, 3CX an FQDN plus extension and
	// password. The admin console renders the form from this, which is what
	// makes a new plugin require no frontend work.
	ConfigSchema json.RawMessage

	Tools []Tool

	// Preflight lets a plugin report which of its tools can currently work.
	// Partial capability is the normal case — an administrator granting only
	// "view tickets" is being sensible — and the right response is for the
	// usable tools to work while the rest explain themselves, rather than for
	// the plugin to fail or for a technician to meet an opaque error mid-task.
	//
	// Optional: nil means every tool is assumed available.
	Preflight Preflight
}

// Error is a controlled error response. Handlers should return these rather
// than wrapping vendor errors, which routinely carry request URLs and auth
// headers.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf builds an [*Error]. The message is sent to the caller and may enter
// model context, so it must contain no credential, hostname or raw vendor
// output.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrMutatingTool is retained for callers that referenced it. Mutating tools
// are now permitted but never reach the model; see [Tool.Mutates].
var ErrMutatingTool = fmt.Errorf("azir: mutating tool")

func (p Plugin) validate() error {
	if p.Name == "" {
		return fmt.Errorf("plugin name is required")
	}
	if p.Version == "" {
		return fmt.Errorf("plugin %q: version is required", p.Name)
	}
	if len(p.Tools) == 0 {
		return fmt.Errorf("plugin %q: declares no tools", p.Name)
	}
	seen := make(map[string]struct{}, len(p.Tools))
	for _, t := range p.Tools {
		if t.Name == "" {
			return fmt.Errorf("plugin %q: tool with empty name", p.Name)
		}
		if _, dup := seen[t.Name]; dup {
			return fmt.Errorf("plugin %q: duplicate tool %q", p.Name, t.Name)
		}
		seen[t.Name] = struct{}{}

		if t.Mutates && t.RequiresPermission == "" {
			return fmt.Errorf("plugin %q: mutating tool %q declares no required permission; "+
				"a write nobody is required to be allowed to make is not a write anyone should make",
				p.Name, t.Name)
		}
		if t.Handler == nil {
			return fmt.Errorf("plugin %q: tool %q has no handler", p.Name, t.Name)
		}
		if err := validateCapabilities(t.Name, t.Provides); err != nil {
			return fmt.Errorf("plugin %q: %w", p.Name, err)
		}
	}
	return nil
}
