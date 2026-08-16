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

	// Mutates marks a tool that writes to an external system. Azir is
	// read-only system-wide, so Serve refuses to start when this is true. The
	// field exists rather than being omitted so that the refusal is explicit
	// and a future contributor meets a boot failure, not a silent success.
	Mutates bool

	// Secrets names Schema properties that must never reach the model.
	Secrets []string

	Handler Handler
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

// ErrMutatingTool is returned by Serve when a plugin declares a mutating tool.
var ErrMutatingTool = fmt.Errorf("azir is read-only: mutating tools cannot be registered")

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

		if t.Mutates {
			return fmt.Errorf("%w: plugin %q tool %q", ErrMutatingTool, p.Name, t.Name)
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
