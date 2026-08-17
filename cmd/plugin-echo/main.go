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
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
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
		Description: "Built-in checks that confirm Azir itself is working",
		Category:    plugin.CategoryInternal,
		// Published to core, which renders the settings form from it. Fields
		// marked x-azir-secret are sealed into the vault instead of the config
		// table, so one admin form can hold both kinds safely.
		ConfigSchema: json.RawMessage(`{
			"type": "object",
			"required": ["greeting"],
			"properties": {
				"greeting": {
					"type": "string",
					"title": "Greeting prefix",
					"description": "Prepended to every ping response.",
					"default": "pong"
				},
				"shout": {
					"type": "boolean",
					"title": "Uppercase the reply"
				},
				"demo_secret": {
					"type": "string",
					"title": "Demo credential",
					"description": "Proves secret routing: stored in the vault, never returned by the API, and redacted from every response.",
					"x-azir-secret": true
				}
			}
		}`),
		Tools: []plugin.Tool{
			{
				Name:        "ping",
				Description: "Confirms Azir can reach this connection and get an answer back.",
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
				Name:        "secret.check",
				Description: "Confirms a stored password can be read back without ever revealing it.",
				Provides:    []plugin.Capability{plugin.CapDiagnostic},
				Schema:      json.RawMessage(`{"type": "object", "properties": {}}`),
				Handler:     secretCheck,
			},
			{
				Name:        "leak",
				Description: "Deliberately tries to return password-shaped text, to prove Azir strips it before anything sees it.",
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

func ping(ctx context.Context, req plugin.Request) (any, error) {
	var args pingArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, plugin.Errorf("400", "arguments could not be parsed")
		}
	}

	// Settings are read from core at request time, so an administrator's
	// change in the console takes effect without restarting anything.
	greeting := "pong"
	shout := false
	if cfg, ok := plugin.ConfigFrom(ctx); ok {
		if values, err := cfg.All(ctx, req.CustomerID); err == nil {
			if g, ok := values["greeting"].(string); ok && g != "" {
				greeting = g
			}
			shout, _ = values["shout"].(bool)
		}
	}

	message := args.Message
	if message == "" {
		message = greeting
	}
	if shout {
		message = strings.ToUpper(message)
	}

	return map[string]any{
		"message":     message,
		"customer_id": req.CustomerID,
		"actor":       req.Actor.UserID,
		"at":          time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// secretCheck proves the vault path end to end: it resolves the demo
// credential and reports only whether resolution worked. The value itself is
// never returned — and would be redacted by the SDK if it were.
func secretCheck(ctx context.Context, req plugin.Request) (any, error) {
	v, ok := plugin.VaultFrom(ctx)
	if !ok {
		return nil, plugin.Errorf("500", "vault unavailable")
	}
	secret, err := v.For(ctx, req.CustomerID, "demo_secret")
	if errors.Is(err, plugin.ErrNoCredential) {
		return map[string]any{
			"configured": false,
			"note":       "set a Demo credential in this plugin's settings",
		}, nil
	}
	if err != nil {
		return nil, plugin.Errorf("502", "credential could not be resolved")
	}
	return map[string]any{
		"configured": true,
		"length":     len(secret),
		"note":       "the value is deliberately not returned",
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
