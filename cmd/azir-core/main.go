// Command azir-core is Azir.
//
// One binary. It embeds a NATS server with JetStream, the HTTP API, the built
// frontend, and a supervisor for bundled plugins; Postgres is the one external
// service. Plugins remain separate processes speaking NATS, so the
// architecture is unchanged.
//
// Point NATS_URL at an external cluster to opt out of the embedded server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/dreulavelle/azir/internal/api"
	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/logging"
	"github.com/dreulavelle/azir/internal/natsd"
	"github.com/dreulavelle/azir/internal/pluginhost"
	"github.com/dreulavelle/azir/internal/registry"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/internal/supervisor"
	"github.com/dreulavelle/azir/internal/vault"
	"github.com/dreulavelle/azir/web"
)

func main() {
	// Every logger in this process — including the ones carrying supervised
	// plugins' stdout — descends from the redacting handler, so a credential
	// cannot reach the container's output even by accident.
	redactor := logging.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()}))
	log := slog.New(redactor)
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dataDir := envOr("AZIR_DATA_DIR", "/var/lib/azir")

	v, err := vault.FromEnv()
	if err != nil {
		return err
	}
	log.Info("vault ready", "key_version", v.CurrentVersion())

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	db, err := store.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Migrate(ctx); err != nil {
		return err
	}
	if applied, err := db.AppliedMigrations(ctx); err == nil && len(applied) > 0 {
		log.Info("store ready", "schema_version", applied[0].Version, "migrations", len(applied))
	}

	// Embedded NATS unless an external one is configured.
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		embedded, err := natsd.Start(natsd.Options{
			StoreDir:    filepath.Join(dataDir, "nats"),
			Host:        envOr("AZIR_NATS_HOST", "127.0.0.1"),
			Port:        envInt("AZIR_NATS_PORT", 4222),
			MonitorPort: envInt("AZIR_NATS_MONITOR_PORT", 0),
			Logger:      log,
		})
		if err != nil {
			return err
		}
		defer embedded.Shutdown()
		natsURL = embedded.URL()
	} else {
		log.Info("using external nats", "url", natsURL)
	}

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

	recorder, js, err := audit.Setup(ctx, nc, log)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := audit.Mirror(ctx, js, db, log); err != nil && ctx.Err() == nil {
			log.Error("audit mirror stopped", "error", err)
		}
	}()

	// Plugins resolve their credentials and settings from core at request
	// time, so an administrator changes them in the console and nothing needs
	// redeploying. These subjects sit outside azir.tool.*, so neither is ever
	// discoverable as a model-facing capability.
	creds := store.NewCredentials(db, v)
	host := pluginhost.New(nc, db, creds, recorder, log)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := host.Start(ctx); err != nil && ctx.Err() == nil {
			log.Error("plugin host stopped", "error", err)
		}
	}()

	reg := registry.New(nc, log, 500*time.Millisecond)
	// Discovery proposes: every tool seen becomes a candidate awaiting an
	// administrator's decision.
	reg.SetObserver(db.Observe)
	wg.Add(1)
	go func() {
		defer wg.Done()
		reg.Run(ctx, 10*time.Second)
	}()

	// Bundled plugins run as supervised children, sharing this process's
	// lifecycle and its redacting logger. Third-party plugins still run
	// wherever they like and connect to the same NATS.
	children, err := supervisor.Discover(envOr("AZIR_PLUGIN_DIR", "/usr/local/lib/azir/plugins"))
	if err != nil {
		log.Warn("could not scan plugin directory", "error", err)
	}
	for i := range children {
		children[i].Env = []string{"NATS_URL=" + natsURL}
	}
	sup := supervisor.New(log, children...)
	wg.Add(1)
	go func() {
		defer wg.Done()
		sup.Run(ctx)
	}()

	assets, err := web.Assets()
	if err != nil {
		log.Warn("frontend assets unavailable; serving api only", "error", err)
		assets = nil
	}

	toolCache := api.NewToolCache(db, log)
	wg.Add(1)
	go func() {
		defer wg.Done()
		toolCache.Prune(ctx, time.Hour, 7*24*time.Hour)
	}()

	srv := &http.Server{
		Addr: envOr("AZIR_HTTP_ADDR", ":8080"),
		Handler: (&api.Server{
			NC:    nc,
			Reg:   reg,
			DB:    db,
			Creds: creds,
			Audit: recorder,
			Log:   log,
			Web:   assets,
			Cache: toolCache,
		}).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("azir listening", "addr", srv.Addr, "plugins", len(children))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown", "error", err)
	}

	// Wait for the supervisor to reap children before the embedded NATS goes
	// away underneath them.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		log.Warn("shutdown timed out waiting for background workers")
	}
	return nil
}

func logLevel() slog.Level {
	switch envOr("AZIR_LOG_LEVEL", "info") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
