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
	"fmt"
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
	"github.com/dreulavelle/azir/internal/scheduler"
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
	// Bundled first, then anywhere else this deployment has been told to look.
	// A site drops a binary into a mounted directory and restarts; nothing is
	// rebuilt, and nothing about approval changes.
	children, err := supervisor.Discover(supervisor.SplitDirs(
		envOr("AZIR_PLUGIN_DIR", "/usr/local/lib/azir/plugins:/opt/azir/plugins"))...)
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
		toolCache.Prune(ctx, time.Hour)
	}()

	/*
		Sign-ins that are over.

		Both halves are already unusable by the time this reaches them — an
		expired session is refused by the query that resolves it, and an
		abandoned sign-in attempt by the one that redeems it — so this keeps
		the tables from growing rather than being what makes either safe.

		PruneSessions had been written and never called, so expired rows had
		been accumulating since the table was made: fifty-three of them against
		two live ones on the deployment where this was noticed. Nothing was
		unsafe, but the count on the Data screen was mostly rows that no longer
		meant anything.
	*/
	wg.Add(1)
	go func() {
		defer wg.Done()
		sweep := func() {
			if n, err := db.PruneSessions(ctx); err != nil {
				log.Warn("could not prune expired sessions", "error", err)
			} else if n > 0 {
				log.Info("pruned expired sessions", "count", n)
			}
		}
		// Once at startup as well, so a deployment that has been off for a
		// fortnight does not wait a quarter of an hour to tidy up.
		sweep()
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweep()
				if n, err := db.PurgeExpiredOIDCStates(ctx); err != nil {
					log.Warn("could not purge abandoned sign-ins", "error", err)
				} else if n > 0 {
					log.Info("purged abandoned sign-ins", "count", n)
				}
			}
		}
	}()

	/*
		Diagnostic captures whose time is up.

		Unlike the sweeps above this one is not housekeeping, it is the
		retention promise being kept. A capture holds a customer's extension
		numbers, MAC addresses, internal addressing and the names of the people
		who administer their phone system; the reason it goes away on its own
		is that nobody should have to remember to delete it. Anything worth
		keeping has been pinned by somebody, and pinning is what clears the
		expiry.

		Hourly, and once at startup, so a deployment that spends a fortnight
		switched off does not come back up serving expired data.
	*/
	wg.Add(1)
	go func() {
		defer wg.Done()
		sweep := func() {
			if n, err := db.SweepSnapshots(ctx); err != nil {
				log.Warn("could not expire diagnostic captures", "error", err)
			} else if n > 0 {
				log.Info("expired diagnostic captures", "count", n)
			}
			// Uploaded sheets go the same way and for the same reason: they
			// hold a customer's extension numbers and the names on them, and
			// the point of one was the change it described.
			if n, err := db.SweepBulkEdits(ctx); err != nil {
				log.Warn("could not expire bulk edits", "error", err)
			} else if n > 0 {
				log.Info("expired bulk edits", "count", n)
			}
		}
		sweep()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweep()
			}
		}
	}()

	/*
		Decisions about tools that no longer exist.

		A capability record is keyed on the plugin's name, and plugins/ hands
		that name out to whatever binary is dropped in under it. Left alone, a
		retired plugin's approvals sit there waiting to be inherited by the
		next thing to claim its name — so a name that has gone quiet for a
		month is forgotten, and anything arriving under it later starts
		pending like any other newcomer.

		Guarded on discovery actually working. Seeing no plugins is what a
		broken NATS connection looks like as well as an empty deployment, and
		the two are worth telling apart before deleting anything.
	*/
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if len(reg.Snapshot().Plugins) == 0 {
					continue
				}
				n, err := db.SweepCapabilities(ctx, store.RetiredAfter)
				if err != nil {
					log.Warn("could not forget retired tools", "error", err)
				} else if n > 0 {
					log.Info("forgot tools nothing has offered in a month", "count", n)
				}
			}
		}
	}()

	server := &api.Server{
		NC:    nc,
		Reg:   reg,
		DB:    db,
		Creds: creds,
		Audit: recorder,
		Log:   log,
		Web:   assets,
		Cache: toolCache,
	}

	/*
		Work somebody asked for, to happen later.

		JetStream holds the timers — a scheduled message survives a restart and
		needs no ticker — and Postgres holds what a person needs to read: what
		is armed, who armed it, and what happened when it fired. Reconcile puts
		the two back in step after a restart before anything is served, so a
		job that lost its timer gets one and a job whose moment passed while
		this was down is marked missed rather than firing hours late.
	*/
	sched, err := scheduler.Setup(ctx, js, db, log, server)
	if err != nil {
		return fmt.Errorf("could not start scheduling: %w", err)
	}
	server.Jobs = sched
	if err := sched.Reconcile(ctx); err != nil {
		log.Error("could not reconcile scheduled work", "error", err)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := sched.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("scheduling stopped", "error", err)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		sched.Sweep(ctx, 5*time.Minute)
	}()

	// Relays "something changed" from wherever it happened to every browser
	// currently looking at a screen it affects.
	stopWatching, err := server.WatchChanges()
	if err != nil {
		return fmt.Errorf("could not watch for changes: %w", err)
	}
	defer stopWatching()

	srv := &http.Server{
		Addr:              envOr("AZIR_HTTP_ADDR", ":8080"),
		Handler:           server.Routes(),
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
