package pluginhost_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/pluginhost"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/internal/testsupport"
	"github.com/dreulavelle/azir/pkg/plugin"
)

var testDSN string

func TestMain(m *testing.M) {
	ctx := context.Background()
	dsn, _, stop, err := testsupport.PostgresDSN(ctx)
	if err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
	testDSN = dsn
	code := m.Run()
	stop()
	os.Exit(code)
}

type harness struct {
	nc    *nats.Conn
	db    *store.DB
	creds *store.Credentials
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	nc, _ := testsupport.NATS(t)
	db := testsupport.DB(t, testDSN)
	creds := store.NewCredentials(db, testsupport.Vault(t))

	recorder, js, err := audit.Setup(ctx, nc, quiet)
	if err != nil {
		t.Fatalf("audit setup: %v", err)
	}
	// The mirror is what moves events from JetStream into Postgres. Without it
	// the recorder publishes into a stream nobody drains, so audit queries
	// return nothing — the harness must run the same components core does.
	go func() { _ = audit.Mirror(ctx, js, db, quiet) }()

	host := pluginhost.New(nc, db, creds, recorder, quiet)
	go func() { _ = host.Start(ctx) }()

	// Wait for the subscriptions to be live.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := nc.Request(plugin.VaultSubject("probe"), []byte(`{"kind":"x"}`), 200*time.Millisecond); err == nil {
			break
		}
	}

	return &harness{nc: nc, db: db, creds: creds}
}

func (h *harness) resolve(t *testing.T, subject string, body any) *nats.Msg {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := h.nc.Request(subject, payload, 3*time.Second)
	if err != nil {
		t.Fatalf("request to %s failed: %v", subject, err)
	}
	return msg
}

// A plugin must not be able to resolve another plugin's credential by claiming
// its name in the request body. The subject is the authority.
func TestCredentialScopeMismatchIsRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.creds.Put(ctx, nil, "syncro", "api_key", []byte("syncro-api-key-value")); err != nil {
		t.Fatal(err)
	}

	// plugin-echo asking, over its own subject, but claiming to be syncro.
	msg := h.resolve(t, plugin.VaultSubject("echo"), map[string]string{
		"plugin": "syncro",
		"kind":   "api_key",
	})

	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "403" {
		t.Fatalf("want 403 for a scope mismatch, got %q", code)
	}
	if string(msg.Data) != "" {
		t.Errorf("a refused resolution returned a body: %q", msg.Data)
	}
}

// Even without a lying body, the subject decides. Asking on echo's subject
// resolves only echo's credentials.
func TestSubjectDeterminesScope(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.creds.Put(ctx, nil, "syncro", "api_key", []byte("syncro-api-key-value")); err != nil {
		t.Fatal(err)
	}

	msg := h.resolve(t, plugin.VaultSubject("echo"), map[string]string{"kind": "api_key"})
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "404" {
		t.Fatalf("echo resolved a credential belonging to syncro (code %q, body %q)", code, msg.Data)
	}
}

func TestCredentialResolves(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	const secret = "echo-demo-credential-value"
	if _, err := h.creds.Put(ctx, nil, "echo", "demo_secret", []byte(secret)); err != nil {
		t.Fatal(err)
	}

	msg := h.resolve(t, plugin.VaultSubject("echo"), map[string]string{"kind": "demo_secret"})
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
		t.Fatalf("resolution failed: %s", msg.Header.Get("Nats-Service-Error"))
	}

	var resp struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Value != secret {
		t.Error("resolved value does not match what was stored")
	}
}

// Per-customer credentials must not bleed between customers.
func TestCustomerScopeIsHonoured(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	acme, err := h.db.CreateCustomer(ctx, "Acme Dental")
	if err != nil {
		t.Fatal(err)
	}
	other, err := h.db.CreateCustomer(ctx, "Other Corp")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := h.creds.Put(ctx, &acme.ID, "echo", "demo_secret", []byte("acme-only-secret")); err != nil {
		t.Fatal(err)
	}

	msg := h.resolve(t, plugin.VaultSubject("echo"), map[string]string{
		"customer_id": other.ID.String(),
		"kind":        "demo_secret",
	})
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "404" {
		t.Fatalf("one customer's credential resolved for another (code %q)", code)
	}
}

// An unconfigured plugin is an ordinary state, not a failure. It must be
// distinguishable so a tool can say "configure me" rather than break.
func TestUnconfiguredCredentialIsNotFound(t *testing.T) {
	h := newHarness(t)

	msg := h.resolve(t, plugin.VaultSubject("echo"), map[string]string{"kind": "never_set"})
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "404" {
		t.Fatalf("want 404 for an unset credential, got %q", code)
	}
}

func TestConfigResolvesAndIsScoped(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if err := h.db.SetPluginConfig(ctx, "echo", nil,
		map[string]any{"greeting": "hello", "shout": true}, "admin"); err != nil {
		t.Fatal(err)
	}

	msg := h.resolve(t, plugin.ConfigSubject("echo"), map[string]string{})
	var resp struct {
		Values map[string]any `json:"values"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Values["greeting"] != "hello" || resp.Values["shout"] != true {
		t.Errorf("settings did not round-trip: %v", resp.Values)
	}

	// A different plugin sees nothing of echo's settings.
	other := h.resolve(t, plugin.ConfigSubject("syncro"), map[string]string{})
	var otherResp struct {
		Values map[string]any `json:"values"`
	}
	if err := json.Unmarshal(other.Data, &otherResp); err != nil {
		t.Fatal(err)
	}
	if len(otherResp.Values) != 0 {
		t.Errorf("another plugin's settings leaked: %v", otherResp.Values)
	}
}

func TestMalformedRequestsAreRefused(t *testing.T) {
	h := newHarness(t)

	// Not JSON.
	msg, err := h.nc.Request(plugin.VaultSubject("echo"), []byte("{not json"), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "400" {
		t.Errorf("want 400 for malformed json, got %q", code)
	}

	// Missing kind.
	msg = h.resolve(t, plugin.VaultSubject("echo"), map[string]string{})
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "400" {
		t.Errorf("want 400 for a missing kind, got %q", code)
	}

	// Unparseable customer id.
	msg = h.resolve(t, plugin.VaultSubject("echo"), map[string]string{
		"customer_id": "not-a-uuid", "kind": "demo_secret",
	})
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "400" {
		t.Errorf("want 400 for a bad customer id, got %q", code)
	}
}

// Resolution must be auditable: who asked, for whom, and for which kind —
// never the value.
func TestResolutionIsAudited(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	const secret = "AZIR-CANARY-audit-b7f3e91d-DO-NOT-EMIT"
	if _, err := h.creds.Put(ctx, nil, "echo", "demo_secret", []byte(secret)); err != nil {
		t.Fatal(err)
	}
	h.resolve(t, plugin.VaultSubject("echo"), map[string]string{"kind": "demo_secret"})

	var events []audit.Event
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		events, err = audit.Recent(ctx, h.db, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	var found bool
	for _, e := range events {
		if e.Action == "credential.resolve" && e.Plugin == "echo" {
			found = true
			if e.ActorUserID != "plugin:echo" {
				t.Errorf("unexpected actor %q", e.ActorUserID)
			}
			if e.Detail != "demo_secret" {
				t.Errorf("audit did not record which kind was resolved: %q", e.Detail)
			}
		}
		if e.Detail == secret {
			t.Fatal("the audit trail recorded the credential value")
		}
	}
	if !found {
		t.Fatalf("credential resolution was not audited; saw %d events", len(events))
	}
}
