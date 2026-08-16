// Command plugin-echo is a diagnostic plugin that exists to prove the
// plumbing: it registers with NATS service discovery, core finds it, and a
// call round-trips.
//
// It also demonstrates the SDK's enforcement. Set AZIR_ECHO_TRY_MUTATE=1 and
// the plugin declares a mutating tool — Serve refuses to start, which is the
// read-only invariant failing loudly rather than quietly.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dreulavelle/azir/pkg/plugin"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := plugin.Plugin{
		Name:        "echo",
		Version:     "0.1.0",
		Description: "Diagnostic plugin used to verify discovery and transport",
		Category:    plugin.CategoryOther,
		ConfigSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"greeting": {"type": "string", "title": "Greeting prefix"}
			}
		}`),
		Tools: []plugin.Tool{
			{
				Name:        "ping",
				Description: "Returns a timestamped acknowledgement. Confirms the plugin is reachable.",
				Provides:    []plugin.Capability{plugin.CapDiagnostic},
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"message": {"type": "string", "description": "Text to echo back"}
					}
				}`),
				Handler: ping,
			},
			{
				Name:        "leak",
				Description: "Deliberately returns credential-shaped fields. Proves SDK redaction runs on the return path.",
				Provides:    []plugin.Capability{plugin.CapDiagnostic},
				Schema:      json.RawMessage(`{"type": "object", "properties": {}}`),
				Handler:     leak,
			},
		},
	}

	// Opt-in proof that a mutating tool cannot be registered.
	if os.Getenv("AZIR_ECHO_TRY_MUTATE") == "1" {
		p.Tools = append(p.Tools, plugin.Tool{
			Name:        "tickets.close",
			Description: "Should never register",
			Provides:    []plugin.Capability{plugin.CapWorkItemsGet},
			Mutates:     true,
			Handler:     ping,
		})
	}

	opts := []plugin.Option{
		plugin.WithLogger(log),
		// A literal secret registered for redaction, standing in for what the
		// vault will supply from phase 1 onward.
		plugin.WithSecrets("super-secret-api-key-value"),
	}

	if err := plugin.Serve(ctx, p, opts...); err != nil {
		log.Error("plugin failed to start", "error", err)
		os.Exit(1)
	}
}

type pingArgs struct {
	Message string `json:"message"`
}

func ping(_ context.Context, req plugin.Request) (any, error) {
	var args pingArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, plugin.Errorf("400", "arguments could not be parsed")
		}
	}
	if args.Message == "" {
		args.Message = "pong"
	}
	return map[string]any{
		"message":     args.Message,
		"customer_id": req.CustomerID,
		"actor":       req.Actor.UserID,
		"at":          time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// leak returns values that must not survive the SDK's outbound redactor: one
// caught by key name, one by literal match against a registered secret.
func leak(_ context.Context, _ plugin.Request) (any, error) {
	return map[string]any{
		"note":        "both fields below should come back redacted",
		"api_key":     "caught-by-key-name",
		"description": "embedded super-secret-api-key-value in prose",
	}, nil
}
