// Command azir-core is the Azir hub: it discovers plugins from the NATS
// service registry, exposes that view over HTTP, and round-trips tool calls.
//
// Phase 0 deliberately contains no business logic. Its job is to prove the
// plumbing: a plugin container appears in discovery, and a call reaches it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/dreulavelle/azir/internal/registry"
	"github.com/dreulavelle/azir/pkg/plugin"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	natsURL := envOr("NATS_URL", nats.DefaultURL)
	addr := envOr("AZIR_HTTP_ADDR", ":8080")

	nc, err := nats.Connect(natsURL,
		nats.Name("azir-core"),
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
	log.Info("connected to nats", "url", nc.ConnectedUrl())

	reg := registry.New(nc, log, 500*time.Millisecond)
	go reg.Run(ctx, 10*time.Second)

	srv := &http.Server{
		Addr:              addr,
		Handler:           routes(nc, reg, log),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func routes(nc *nats.Conn, reg *registry.Registry, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		status := "ok"
		code := http.StatusOK
		if !nc.IsConnected() {
			status, code = "nats disconnected", http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]string{"status": status})
	})

	// The discovered view of the deployment. In phase 1 this gains an
	// approved/pending distinction; today everything discovered is a candidate.
	mux.HandleFunc("GET /api/registry", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, reg.Snapshot())
	})

	// Which tools satisfy a capability. Features use this to report themselves
	// unavailable with a reason rather than failing opaquely.
	mux.HandleFunc("GET /api/capabilities/{cap}", func(w http.ResponseWriter, r *http.Request) {
		c := plugin.Capability(r.PathValue("cap"))
		writeJSON(w, http.StatusOK, map[string]any{
			"capability": c,
			"known":      c.Valid(),
			"providers":  reg.Providers(c),
		})
	})

	mux.HandleFunc("POST /api/invoke/{plugin}/{tool}", func(w http.ResponseWriter, r *http.Request) {
		invoke(w, r, nc, reg, log)
	})

	return mux
}

// invokeRequest is the HTTP shape of a tool call. Actor is stubbed here; phase
// 6 supplies it from the authenticated session, and it is never trusted from
// the client.
type invokeRequest struct {
	CustomerID string          `json:"customer_id"`
	Args       json.RawMessage `json:"args,omitempty"`
}

func invoke(w http.ResponseWriter, r *http.Request, nc *nats.Conn, reg *registry.Registry, log *slog.Logger) {
	pluginName := r.PathValue("plugin")
	toolName := r.PathValue("tool")

	tool, ok := reg.Lookup(pluginName, toolName)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "no such tool in the current registry snapshot",
		})
		return
	}

	var body invokeRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}
	}

	payload, err := json.Marshal(plugin.Request{
		CustomerID: body.CustomerID,
		Actor:      plugin.Actor{UserID: "phase0", Role: "admin"},
		Args:       body.Args,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "encode failed"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	msg, err := nc.RequestWithContext(ctx, tool.Subject, payload)
	if err != nil {
		log.Warn("tool request failed", "subject", tool.Subject, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "tool did not respond"})
		return
	}

	// micro reports handler errors via headers rather than the body.
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": msg.Header.Get("Nats-Service-Error"),
			"code":  code,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(msg.Data)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
