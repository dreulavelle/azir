// Package natsd runs a NATS server inside the core process.
//
// Embedding removes a container without touching the plugin architecture:
// plugins still register as NATS micro services and still speak the same
// protocol, they just reach a server living in the same process. Setting
// NATS_URL points everything at an external cluster instead, so the single-
// binary default never becomes a ceiling.
package natsd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// Server wraps an in-process NATS server.
type Server struct {
	srv *natsserver.Server
	url string
}

// Options configures the embedded server.
type Options struct {
	// StoreDir is where JetStream persists. Required.
	StoreDir string
	// Host and Port for the client listener. Port 0 picks a free one, which
	// is what tests want; -1 disables TCP entirely.
	Host string
	Port int
	// MonitorPort exposes /healthz and /varz when non-zero.
	MonitorPort int
	Logger      *slog.Logger
}

// Start launches the server and waits for it to accept connections.
func Start(opts Options) (*Server, error) {
	if opts.StoreDir == "" {
		return nil, fmt.Errorf("natsd: store directory is required")
	}
	if err := os.MkdirAll(opts.StoreDir, 0o750); err != nil {
		return nil, fmt.Errorf("natsd: create store dir: %w", err)
	}
	if opts.Host == "" {
		// Bound to loopback by default: the embedded server exists for
		// in-process and sidecar plugins, not as a public endpoint. Set
		// AZIR_NATS_HOST deliberately to expose it.
		opts.Host = "127.0.0.1"
	}

	cfg := &natsserver.Options{
		ServerName:         "azir-embedded",
		Host:               opts.Host,
		Port:               opts.Port,
		JetStream:          true,
		JetStreamMaxMemory: -1,
		JetStreamMaxStore:  -1,
		StoreDir:           opts.StoreDir,
		NoSigs:             true,
		NoLog:              true,
		/*
			Larger than the one-megabyte default, for one reply that needs it.

			A pulled diagnostic capture is a support bundle read where the
			credentials are, and what comes back is the report rather than the
			forty megabytes it was read from — a few hundred kilobytes on the
			systems seen so far, but bounded by how many distinct findings a
			phone system has rather than by anything fixed. Eight megabytes is
			room for a much worse one; every other reply here is a page of JSON
			and nowhere near it.
		*/
		MaxPayload: 8 << 20,
	}
	if opts.MonitorPort > 0 {
		cfg.HTTPHost = opts.Host
		cfg.HTTPPort = opts.MonitorPort
	}

	srv, err := natsserver.NewServer(cfg)
	if err != nil {
		return nil, fmt.Errorf("natsd: new server: %w", err)
	}

	go srv.Start()
	if !srv.ReadyForConnections(15 * time.Second) {
		srv.Shutdown()
		return nil, fmt.Errorf("natsd: server did not become ready")
	}

	if opts.Logger != nil {
		opts.Logger.Info("embedded nats started",
			"url", srv.ClientURL(),
			"jetstream", true,
			"store_dir", filepath.Clean(opts.StoreDir))
	}
	return &Server{srv: srv, url: srv.ClientURL()}, nil
}

// URL is the client connection URL.
func (s *Server) URL() string { return s.url }

// Shutdown stops the server and waits for it to finish.
func (s *Server) Shutdown() {
	s.srv.Shutdown()
	s.srv.WaitForShutdown()
}
