package registry_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/dreulavelle/azir/internal/registry"
	"github.com/dreulavelle/azir/pkg/plugin"
)

// startNATS runs an in-process NATS server so the round trip is proven against
// the real protocol rather than a mock.
func startNATS(t *testing.T) string {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("start nats: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server not ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// servePlugin starts a plugin and waits until it answers discovery.
func servePlugin(t *testing.T, url string, p plugin.Plugin) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() {
		errCh <- plugin.Serve(ctx, p, plugin.WithNATSURL(url), plugin.WithLogger(quietLogger()))
	}()

	select {
	case err := <-errCh:
		t.Fatalf("plugin exited early: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
}

func testPlugin() plugin.Plugin {
	return plugin.Plugin{
		Name:        "echo",
		Version:     "0.1.0",
		Description: "test fixture",
		Category:    plugin.CategoryOther,
		Tools: []plugin.Tool{
			{
				Name:        "ping",
				Description: "returns pong",
				Provides:    []plugin.Capability{plugin.CapDiagnostic},
				Schema:      json.RawMessage(`{"type":"object"}`),
				Handler: func(_ context.Context, req plugin.Request) (any, error) {
					return map[string]any{"pong": true, "customer": req.CustomerID}, nil
				},
			},
			{
				// A dotted name: NATS micro rejects dots in endpoint names, so
				// the SDK sanitises the endpoint and keeps the dots in the
				// subject. Every real tool name is dotted, so this must work.
				Name:        "tickets.search",
				Description: "dotted name",
				Provides:    []plugin.Capability{plugin.CapDiagnostic},
				Handler: func(_ context.Context, _ plugin.Request) (any, error) {
					return map[string]any{"dotted": true}, nil
				},
			},
			{
				Name:     "boom",
				Provides: []plugin.Capability{plugin.CapDiagnostic},
				Handler: func(_ context.Context, _ plugin.Request) (any, error) {
					// An unwrapped error carrying credential-shaped detail.
					return nil, errNaked{}
				},
			},
		},
	}
}

func payloadFor(customerID string) []byte {
	body, _ := json.Marshal(plugin.Request{CustomerID: customerID})
	return body
}

type errNaked struct{}

func (errNaked) Error() string {
	return "GET https://pbx.example.com/api?password=hunter2 failed: 401"
}

// The phase 0 exit criterion: core discovers a plugin through $SRV and a call
// round-trips to it.
func TestDiscoveryAndRoundTrip(t *testing.T) {
	url := startNATS(t)
	servePlugin(t, url, testPlugin())

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	reg := registry.New(nc, quietLogger(), 500*time.Millisecond)
	if err := reg.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	snap := reg.Snapshot()
	if len(snap.Plugins) != 1 {
		t.Fatalf("want 1 plugin discovered, got %d", len(snap.Plugins))
	}
	got := snap.Plugins[0]
	if got.Name != "echo" || got.Version != "0.1.0" {
		t.Fatalf("unexpected identity: %+v", got)
	}
	if got.SDK != plugin.SDKVersion {
		t.Errorf("want sdk %q, got %q", plugin.SDKVersion, got.SDK)
	}
	if len(got.Tools) != 3 {
		t.Fatalf("want 3 tools, got %d", len(got.Tools))
	}

	// Capability index must point at the right tool.
	providers := reg.Providers(plugin.CapDiagnostic)
	if len(providers) != 3 {
		t.Fatalf("want 3 providers of %s, got %v", plugin.CapDiagnostic, providers)
	}

	// A dotted tool name must survive registration and discovery intact, and
	// its subject must keep the dots.
	dotted, ok := reg.Lookup("echo", "tickets.search")
	if !ok {
		t.Fatal("a dotted tool name did not survive discovery")
	}
	if dotted.Subject != "azir.tool.echo.tickets.search" {
		t.Errorf("unexpected subject for dotted tool: %q", dotted.Subject)
	}
	if _, err := nc.Request(dotted.Subject, payloadFor("cust_1"), 3*time.Second); err != nil {
		t.Errorf("dotted tool is not reachable: %v", err)
	}

	tool, ok := reg.Lookup("echo", "ping")
	if !ok {
		t.Fatal("ping not found in registry")
	}
	if tool.Description != "returns pong" {
		t.Errorf("metadata did not survive discovery: %q", tool.Description)
	}

	msg, err := nc.Request(tool.Subject, payloadFor("cust_123"), 3*time.Second)
	if err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(msg.Data, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["pong"] != true {
		t.Errorf("unexpected body: %v", body)
	}
	if body["customer"] != "cust_123" {
		t.Errorf("customer id did not reach the handler: %v", body)
	}
}

// An unrecognised error must never be echoed to the caller: vendor errors
// routinely carry URLs, hosts and credentials.
func TestUnwrappedErrorIsNotEchoed(t *testing.T) {
	url := startNATS(t)
	servePlugin(t, url, testPlugin())

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	reg := registry.New(nc, quietLogger(), 500*time.Millisecond)
	if err := reg.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Lookup("echo", "boom")
	if !ok {
		t.Fatal("boom not found")
	}

	msg, err := nc.Request(tool.Subject, payloadFor("cust_123"), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	desc := msg.Header.Get("Nats-Service-Error")
	full := desc + string(msg.Data)
	for _, leaked := range []string{"hunter2", "pbx.example.com", "password"} {
		if strings.Contains(full, leaked) {
			t.Errorf("vendor error detail %q escaped to the caller: %q", leaked, full)
		}
	}
	if desc != "internal plugin error" {
		t.Errorf("want generic error description, got %q", desc)
	}
}

// The capability index must key on real tool names. The endpoint name has its
// dots stripped for micro, and the approval gate keys on the real name — so a
// sanitised name here produces provider keys that nothing else can match.
func TestCapabilityIndexUsesRealToolNames(t *testing.T) {
	url := startNATS(t)
	servePlugin(t, url, testPlugin())

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	reg := registry.New(nc, quietLogger(), 500*time.Millisecond)
	if err := reg.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	providers := reg.Providers(plugin.CapDiagnostic)
	for _, p := range providers {
		if strings.Contains(p, "_") {
			t.Errorf("capability index carries a sanitised name %q; "+
				"approval keys use dots and will never match", p)
		}
	}
	if !slices.Contains(providers, "echo.tickets.search") {
		t.Errorf("dotted tool missing from the capability index: %v", providers)
	}
}
