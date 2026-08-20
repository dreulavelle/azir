package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/dreulavelle/azir/internal/api"
	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/registry"
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

// server brings up the HTTP surface against a real database, a real NATS and
// two real plugins: one that can write and one that cannot. The write switch is
// meaningless without a registry that knows which is which, so the test runs
// the same discovery core does rather than a stand-in for it.
func server(t *testing.T) (*httptest.Server, *http.Client) {
	srv, client, _ := serverWithDB(t)
	return srv, client
}

// serverWithDB is the same harness, handing back the database as well, for the
// tests whose subject is what is stored rather than what an endpoint answers.
func serverWithDB(t *testing.T) (*httptest.Server, *http.Client, *store.DB) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	nc, url := testsupport.NATS(t)
	db := testsupport.DB(t, testDSN)

	recorder, js, err := audit.Setup(ctx, nc, quiet)
	if err != nil {
		t.Fatalf("audit setup: %v", err)
	}
	go func() { _ = audit.Mirror(ctx, js, db, quiet) }()

	writer := plugin.Plugin{
		Name: "writer", Version: "0.0.1", Description: "has a tool that writes",
		Category: plugin.CategoryPSA,
		Tools: []plugin.Tool{
			{
				Name: "thing.read", Description: "reads",
				Provides: []plugin.Capability{plugin.CapWorkItemsGet},
				Handler: func(context.Context, plugin.Request) (any, error) {
					return map[string]string{"ok": "read"}, nil
				},
			},
			{
				// It declares a capability like any other tool. Exclusion from
				// the index is core's doing, not an omission by the plugin —
				// which is the property the index test is checking.
				Name: "thing.write", Description: "writes",
				Provides: []plugin.Capability{plugin.CapWorkItemsGet},
				Mutates:  true, RequiresPermission: "ticket.comment",
				Handler: func(context.Context, plugin.Request) (any, error) {
					return map[string]string{"ok": "written"}, nil
				},
			},
		},
	}
	reader := plugin.Plugin{
		Name: "reader", Version: "0.0.1", Description: "reads only",
		Category: plugin.CategoryOther,
		Tools: []plugin.Tool{
			{
				Name: "thing.read", Description: "reads",
				Provides: []plugin.Capability{plugin.CapWorkItemsGet},
				Handler: func(context.Context, plugin.Request) (any, error) {
					return map[string]string{"ok": "read"}, nil
				},
			},
		},
	}
	for _, p := range []plugin.Plugin{writer, reader} {
		go func() {
			if err := plugin.Serve(ctx, p, plugin.WithNATSURL(url), plugin.WithLogger(quiet)); err != nil && ctx.Err() == nil {
				t.Errorf("serving %s: %v", p.Name, err)
			}
		}()
	}

	reg := registry.New(nc, quiet, 500*time.Millisecond)
	// Discovery proposes candidates; without the observer nothing records them,
	// and an administrator has nothing to approve. Core wires this, so the
	// harness must too or it is testing a different system.
	reg.SetObserver(db.Observe)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := reg.Refresh(ctx); err == nil && len(reg.Snapshot().Plugins) == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n := len(reg.Snapshot().Plugins); n != 2 {
		t.Fatalf("discovery found %d plugins, want 2", n)
	}

	s := &api.Server{
		NC: nc, Reg: reg, DB: db,
		Creds: store.NewCredentials(db, testsupport.Vault(t), nil),
		Audit: recorder, Log: quiet,
	}
	srv := httptest.NewServer(s.Routes())
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}

	// First-run setup both creates the administrator and returns the session,
	// so the client is signed in from here on.
	do(t, client, http.MethodPost, srv.URL+"/api/setup", map[string]string{
		"email": "admin@test.local", "display_name": "Admin", "password": "test-password-1234",
	}, http.StatusOK)

	return srv, client, db
}

func do(t *testing.T, c *http.Client, method, url string, body any, wantStatus int) map[string]any {
	t.Helper()

	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, payload)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close() //nolint:errcheck // test

	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != wantStatus {
		t.Fatalf("%s %s: status %d, want %d: %s", method, url, res.StatusCode, wantStatus, raw)
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// The write switch is the single decision that moves Azir from reading a
// customer's systems to changing them. It has its own endpoint precisely so
// that saving an unrelated setting cannot move it — in either direction.
func TestSettingsFormCannotMoveTheWriteSwitch(t *testing.T) {
	srv, client := server(t)
	settingsURL := srv.URL + "/api/plugins/writer/settings"
	writesURL := srv.URL + "/api/plugins/writer/writes"

	writesEnabled := func() bool {
		view := do(t, client, http.MethodGet, settingsURL, nil, http.StatusOK)
		enabled, _ := view["writes_enabled"].(bool)
		return enabled
	}

	view := do(t, client, http.MethodGet, settingsURL, nil, http.StatusOK)
	if has, _ := view["has_mutating_tools"].(bool); !has {
		t.Error("a plugin with a mutating tool did not report having one")
	}
	if writesEnabled() {
		t.Fatal("writes were enabled without anyone enabling them")
	}

	// The form cannot switch writes ON.
	do(t, client, http.MethodPut, settingsURL,
		map[string]any{"values": map[string]any{"writes_enabled": true, "region": "eu"}},
		http.StatusOK)
	if writesEnabled() {
		t.Error("the settings form switched writes on")
	}

	do(t, client, http.MethodPut, writesURL, map[string]any{"enabled": true}, http.StatusOK)
	if !writesEnabled() {
		t.Fatal("the write switch did not turn on")
	}

	// Saving an unrelated field must not clear it. This is the regression that
	// matters most: the switch lives in the same row as the plugin's settings,
	// and a whole-map write would silently reset it.
	do(t, client, http.MethodPut, settingsURL,
		map[string]any{"values": map[string]any{"region": "us"}}, http.StatusOK)
	if !writesEnabled() {
		t.Error("saving an unrelated setting turned writes off")
	}

	// Nor may the form switch them OFF, so that the audit log has exactly one
	// kind of entry for this decision.
	do(t, client, http.MethodPut, settingsURL,
		map[string]any{"values": map[string]any{"writes_enabled": false, "region": "us"}},
		http.StatusOK)
	if !writesEnabled() {
		t.Error("the settings form switched writes off")
	}

	do(t, client, http.MethodPut, writesURL, map[string]any{"enabled": false}, http.StatusOK)
	if writesEnabled() {
		t.Error("the write switch did not turn off")
	}
}

// Offering the switch on a plugin that cannot write would tell an administrator
// their deployment is one toggle away from writing when it is not.
func TestWriteSwitchIsRefusedForAPluginWithNoWrites(t *testing.T) {
	srv, client := server(t)

	view := do(t, client, http.MethodGet, srv.URL+"/api/plugins/reader/settings", nil, http.StatusOK)
	if has, _ := view["has_mutating_tools"].(bool); has {
		t.Error("a read-only plugin reported having a mutating tool")
	}

	do(t, client, http.MethodPut, srv.URL+"/api/plugins/reader/writes",
		map[string]any{"enabled": true}, http.StatusBadRequest)
}

// A mutating tool must not appear in the capability index, which is what a
// feature — and later the agent — consults to find a tool for a job.
func TestMutatingToolsAreNotDiscoverableByCapability(t *testing.T) {
	srv, client := server(t)

	body := do(t, client, http.MethodGet, srv.URL+"/api/registry", nil, http.StatusOK)
	caps, _ := body["capabilities"].(map[string]any)
	for capability, providers := range caps {
		list, _ := providers.([]any)
		for _, p := range list {
			if p == "writer.thing.write" {
				t.Errorf("a mutating tool is reachable through capability %q", capability)
			}
		}
	}
}

// The containment property the whole assistant design rests on.
//
// Ticket text is attacker-controlled: anyone who can email a helpdesk can put
// instructions in front of the model. Safety therefore cannot depend on the
// model declining, because a model that can be asked can eventually be
// persuaded. It depends on there being no action to reach — so a tool that
// writes must be absent from what the model is offered, under every
// configuration, including one where an administrator has switched writes on.
func TestTheAssistantIsNeverOfferedAToolThatWrites(t *testing.T) {
	srv, client := server(t)

	// Approve everything, including the write, and switch writes on. This is
	// the most permissive state a deployment can be in.
	body := do(t, client, http.MethodGet, srv.URL+"/api/registry", nil, http.StatusOK)
	plugins, _ := body["plugins"].([]any)
	for _, p := range plugins {
		pm, _ := p.(map[string]any)
		name, _ := pm["name"].(string)
		tools, _ := pm["tools"].([]any)
		for _, raw := range tools {
			tm, _ := raw.(map[string]any)
			tool, _ := tm["name"].(string)
			do(t, client, http.MethodPost,
				fmt.Sprintf("%s/api/capabilities/%s/%s/decide", srv.URL, name, tool),
				map[string]string{"status": "approved"}, http.StatusOK)
		}
	}
	do(t, client, http.MethodPut, srv.URL+"/api/plugins/writer/writes",
		map[string]any{"enabled": true}, http.StatusOK)

	// What the assistant would be offered, reported by its own settings.
	view := do(t, client, http.MethodGet, srv.URL+"/api/assistant/settings", nil, http.StatusOK)
	offered, _ := view["available_lookups"].([]any)

	names := make([]string, 0, len(offered))
	for _, o := range offered {
		if s, ok := o.(string); ok {
			names = append(names, s)
		}
	}
	if len(names) == 0 {
		t.Fatal("the assistant was offered nothing at all, so this proves nothing")
	}

	// The writing tool declares work_items.get like every other ticket tool, so
	// its exclusion cannot be an accident of it having no capability.
	for _, n := range names {
		if n == "work_items.get" {
			// This capability is provided by the read tool too. Its presence is
			// fine; what matters is that asking for it cannot reach the write.
			continue
		}
	}

	// Resolution must land on a tool that does not write.
	for _, n := range names {
		tool, err := resolveForTest(t, srv, client, n)
		if err != nil {
			continue
		}
		if tool {
			t.Errorf("capability %q resolves to a tool that writes", n)
		}
	}
}

// resolveForTest reports whether the capability routes to a mutating tool, by
// asking the registry the same question the assistant's runner does.
func resolveForTest(t *testing.T, srv *httptest.Server, client *http.Client, capability string) (bool, error) {
	t.Helper()
	body := do(t, client, http.MethodGet, srv.URL+"/api/registry", nil, http.StatusOK)
	caps, _ := body["capabilities"].(map[string]any)
	providers, _ := caps[capability].([]any)
	for _, p := range providers {
		qualified, _ := p.(string)
		plugins, _ := body["plugins"].([]any)
		for _, raw := range plugins {
			pm, _ := raw.(map[string]any)
			name, _ := pm["name"].(string)
			tools, _ := pm["tools"].([]any)
			for _, rawTool := range tools {
				tm, _ := rawTool.(map[string]any)
				toolName, _ := tm["name"].(string)
				if name+"."+toolName != qualified {
					continue
				}
				if mutates, _ := tm["mutates"].(bool); mutates {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
