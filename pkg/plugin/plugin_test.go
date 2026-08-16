package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spoked/azir/pkg/plugin"
)

func noop(_ context.Context, _ plugin.Request) (any, error) { return nil, nil }

func validTool() plugin.Tool {
	return plugin.Tool{
		Name:     "ping",
		Provides: []plugin.Capability{plugin.CapDiagnostic},
		Handler:  noop,
	}
}

// The read-only invariant: a plugin declaring a mutating tool must fail to
// boot, not fail quietly at request time.
func TestServeRefusesMutatingTool(t *testing.T) {
	p := plugin.Plugin{
		Name:    "writer",
		Version: "0.1.0",
		Tools: []plugin.Tool{
			validTool(),
			{
				Name:     "tickets.close",
				Provides: []plugin.Capability{plugin.CapWorkItemsGet},
				Mutates:  true,
				Handler:  noop,
			},
		},
	}

	err := plugin.Serve(context.Background(), p)
	if err == nil {
		t.Fatal("Serve accepted a mutating tool; the read-only invariant is not enforced")
	}
	if !errors.Is(err, plugin.ErrMutatingTool) {
		t.Fatalf("want ErrMutatingTool, got %v", err)
	}
	// Must fail before touching the network, so a misconfigured plugin cannot
	// briefly appear in discovery.
	if strings.Contains(err.Error(), "connect") {
		t.Fatalf("validation ran after connecting: %v", err)
	}
}

func TestPluginValidation(t *testing.T) {
	tests := []struct {
		name string
		p    plugin.Plugin
		want string
	}{
		{
			name: "missing name",
			p:    plugin.Plugin{Version: "0.1.0", Tools: []plugin.Tool{validTool()}},
			want: "plugin name is required",
		},
		{
			name: "missing version",
			p:    plugin.Plugin{Name: "x", Tools: []plugin.Tool{validTool()}},
			want: "version is required",
		},
		{
			name: "no tools",
			p:    plugin.Plugin{Name: "x", Version: "0.1.0"},
			want: "declares no tools",
		},
		{
			name: "unknown capability",
			p: plugin.Plugin{Name: "x", Version: "0.1.0", Tools: []plugin.Tool{{
				Name:     "t",
				Provides: []plugin.Capability{"invented.tag"},
				Handler:  noop,
			}}},
			want: "unknown capability",
		},
		{
			name: "no capabilities",
			p: plugin.Plugin{Name: "x", Version: "0.1.0", Tools: []plugin.Tool{{
				Name:    "t",
				Handler: noop,
			}}},
			want: "declares no capabilities",
		},
		{
			name: "duplicate tool",
			p: plugin.Plugin{Name: "x", Version: "0.1.0", Tools: []plugin.Tool{
				validTool(), validTool(),
			}},
			want: "duplicate tool",
		},
		{
			name: "missing handler",
			p: plugin.Plugin{Name: "x", Version: "0.1.0", Tools: []plugin.Tool{{
				Name:     "t",
				Provides: []plugin.Capability{plugin.CapDiagnostic},
			}}},
			want: "has no handler",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := plugin.Serve(context.Background(), tc.p)
			if err == nil {
				t.Fatal("expected validation failure")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %q", tc.want, err)
			}
		})
	}
}

func TestRedactorByKeyName(t *testing.T) {
	r := plugin.NewRedactor()
	out, err := r.Value(map[string]any{
		"safe":          "visible",
		"api_key":       "abcdef123456",
		"Authorization": "Bearer xyz",
		"nested": map[string]any{
			"client_secret": "shhh",
			"ticket_id":     42,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	got := string(raw)

	for _, leaked := range []string{"abcdef123456", "Bearer xyz", "shhh"} {
		if strings.Contains(got, leaked) {
			t.Errorf("redactor leaked %q: %s", leaked, got)
		}
	}
	if !strings.Contains(got, "visible") {
		t.Errorf("redactor destroyed non-sensitive data: %s", got)
	}
	if !strings.Contains(got, "42") {
		t.Errorf("redactor destroyed non-sensitive nested data: %s", got)
	}
}

// A credential can escape by being embedded in prose that key-name rules never
// inspect, so registered literals are scrubbed wherever they appear.
func TestRedactorByLiteral(t *testing.T) {
	const secret = "super-secret-value"
	r := plugin.NewRedactor(secret)

	out, err := r.Value(map[string]any{
		"message": "connection to host failed using " + secret + " as the token",
		"list":    []any{"prefix " + secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), secret) {
		t.Fatalf("literal secret survived redaction: %s", raw)
	}
}

// Very short literals would match everywhere and destroy the payload.
func TestRedactorIgnoresShortLiterals(t *testing.T) {
	r := plugin.NewRedactor("ab")
	out, err := r.Value(map[string]any{"message": "abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "abcdef") {
		t.Fatalf("short literal was applied and mangled output: %s", raw)
	}
}
