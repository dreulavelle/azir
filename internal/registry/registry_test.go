package registry_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"sort"
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

// A mutating tool must never appear in the capability index. The index is what
// a feature — and later the agent — consults to find a tool, so a write that
// appeared there would be discoverable by exactly the thing that must never
// reach one.
func TestMutatingToolsStayOutOfTheCapabilityIndex(t *testing.T) {
	url := startNATS(t)

	p := testPlugin()
	p.Tools = append(p.Tools, plugin.Tool{
		Name:               "tickets.comment",
		Description:        "writes",
		Provides:           []plugin.Capability{plugin.CapDiagnostic},
		Mutates:            true,
		RequiresPermission: "ticket.comment",
		Handler: func(_ context.Context, _ plugin.Request) (any, error) {
			return map[string]any{"written": true}, nil
		},
	})
	servePlugin(t, url, p)

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	reg := registry.New(nc, quietLogger(), 500*time.Millisecond)
	if err := reg.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, provider := range reg.Providers(plugin.CapDiagnostic) {
		if strings.Contains(provider, "comment") {
			t.Errorf("a mutating tool reached the capability index: %s", provider)
		}
	}

	// It is still discoverable to an interface, with its permission attached.
	tool, ok := reg.Lookup("echo", "tickets.comment")
	if !ok {
		t.Fatal("the mutating tool vanished entirely; it should be visible, just not to the model")
	}
	if !tool.Mutates || tool.RequiresPermission != "ticket.comment" {
		t.Errorf("mutation metadata lost: %+v", tool)
	}
}

// The two indexes must never overlap.
//
// Reading and changing are separate doors on purpose: /api/do resolves through
// Providers and /api/change through WriteProviders, and the whole containment
// argument is that no way of reading a thing can return a way of writing it. A
// tool appearing in both would collapse that silently, with nothing failing.
func TestReadAndWriteIndexesDoNotOverlap(t *testing.T) {
	url := startNATS(t)

	p := testPlugin()
	p.Tools = append(p.Tools,
		plugin.Tool{
			Name:               "tickets.comment",
			Description:        "writes",
			Provides:           []plugin.Capability{plugin.CapWorkItemsComment},
			Mutates:            true,
			RequiresPermission: "ticket.comment",
			Handler: func(_ context.Context, _ plugin.Request) (any, error) {
				return map[string]any{"written": true}, nil
			},
		},
		plugin.Tool{
			Name:        "tickets.read",
			Description: "reads",
			Provides:    []plugin.Capability{plugin.CapWorkItemsGet},
			Handler: func(_ context.Context, _ plugin.Request) (any, error) {
				return map[string]any{"read": true}, nil
			},
		},
	)
	servePlugin(t, url, p)

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	reg := registry.New(nc, quietLogger(), 500*time.Millisecond)
	if err := reg.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The writer is reachable for changing, and only for changing.
	writers := reg.WriteProviders(plugin.CapWorkItemsComment)
	if len(writers) == 0 {
		t.Fatal("the mutating tool is unreachable for writing, so no button could ever use it")
	}
	if got := reg.Providers(plugin.CapWorkItemsComment); len(got) != 0 {
		t.Errorf("a write capability resolved through the read index: %v", got)
	}

	// The reader is reachable for reading, and never for changing.
	if len(reg.Providers(plugin.CapWorkItemsGet)) == 0 {
		t.Fatal("the read tool is unreachable for reading")
	}
	if got := reg.WriteProviders(plugin.CapWorkItemsGet); len(got) != 0 {
		t.Errorf("a read tool answered a request to change something: %v", got)
	}

	// And nothing at all appears on both sides.
	read := map[string]bool{}
	for _, c := range []plugin.Capability{plugin.CapWorkItemsGet, plugin.CapWorkItemsComment, plugin.CapDiagnostic} {
		for _, provider := range reg.Providers(c) {
			read[provider] = true
		}
		for _, provider := range reg.WriteProviders(c) {
			if read[provider] {
				t.Errorf("%s is in both the read and the write index", provider)
			}
		}
	}
}

// Two tools answering to one capability is answered by whichever sorts first.
//
// That is deterministic, which is the property the resolver was designed for,
// and it is also how a request for a phone system's health came back as a list
// of log lines: "events.recent" sorts before "system.status", both claimed
// phone_system.status, and the screen crashed on a shape it had no reason to
// expect. Deterministic is not the same as correct, so a collision is worth
// being able to see.
func TestCapabilityWithSeveralProvidersIsVisible(t *testing.T) {
	url := startNATS(t)

	p := testPlugin()
	p.Tools = append(p.Tools,
		plugin.Tool{
			Name:        "aaa.first",
			Description: "sorts first",
			Provides:    []plugin.Capability{plugin.CapPhoneStatus},
			Handler: func(_ context.Context, _ plugin.Request) (any, error) {
				return map[string]any{"who": "first"}, nil
			},
		},
		plugin.Tool{
			Name:        "zzz.second",
			Description: "sorts last",
			Provides:    []plugin.Capability{plugin.CapPhoneStatus},
			Handler: func(_ context.Context, _ plugin.Request) (any, error) {
				return map[string]any{"who": "second"}, nil
			},
		},
	)
	servePlugin(t, url, p)

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	reg := registry.New(nc, quietLogger(), 500*time.Millisecond)
	if err := reg.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	providers := reg.Providers(plugin.CapPhoneStatus)
	if len(providers) != 2 {
		t.Fatalf("got %d providers, want both: %v", len(providers), providers)
	}
	// Sorted, so the choice is stable rather than whichever replied first.
	if !sort.StringsAreSorted(providers) {
		t.Errorf("providers are not sorted, so the same call could route two ways: %v", providers)
	}
	if !strings.Contains(providers[0], "aaa.first") {
		t.Errorf("the first provider is %q; resolution does not follow the sort", providers[0])
	}
}
