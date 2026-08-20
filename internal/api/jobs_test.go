package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/dreulavelle/azir/internal/api"
	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/registry"
	"github.com/dreulavelle/azir/internal/scheduler"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/internal/testsupport"
	"github.com/dreulavelle/azir/pkg/plugin"
)

/*
Work somebody asked for, to happen later.

These run the whole path: an HTTP request arms a job, JetStream holds the
timer, and the firing goes back through the same gate a button press does. The
subject is not that a timer works — it is that a change made with nobody
present is still a change somebody was allowed to make, and that everything
which could take that permission away still does.
*/

// scheduled brings up the surface with scheduling wired, and hands back a way
// to count what actually reached the plugin.
func scheduled(t *testing.T) (*httptest.Server, *http.Client, *store.DB, *atomic.Int64) {
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

	// Counted rather than mocked: the question is how many times a customer's
	// system was actually changed, and only the far end can answer that.
	var calls atomic.Int64
	p := plugin.Plugin{
		Name: "writer", Version: "0.0.1", Description: "has a tool that writes",
		Category: plugin.CategoryPSA,
		Tools: []plugin.Tool{{
			Name: "thing.write", Description: "writes",
			Provides: []plugin.Capability{plugin.CapWorkItemsGet},
			Mutates:  true, RequiresPermission: "ticket.comment",
			Handler: func(context.Context, plugin.Request) (any, error) {
				calls.Add(1)
				return map[string]string{"ok": "written"}, nil
			},
		}},
	}
	go func() {
		if err := plugin.Serve(ctx, p, plugin.WithNATSURL(url), plugin.WithLogger(quiet)); err != nil && ctx.Err() == nil {
			t.Errorf("serving %s: %v", p.Name, err)
		}
	}()

	reg := registry.New(nc, quiet, 500*time.Millisecond)
	reg.SetObserver(db.Observe)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := reg.Refresh(ctx); err == nil && len(reg.Snapshot().Plugins) == 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n := len(reg.Snapshot().Plugins); n != 1 {
		t.Fatalf("discovery found %d plugins, want 1", n)
	}

	s := &api.Server{
		NC: nc, Reg: reg, DB: db,
		Creds: store.NewCredentials(db, testsupport.Vault(t)),
		Audit: recorder, Log: quiet,
	}
	stream, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	sched, err := scheduler.Setup(ctx, stream, db, quiet, s)
	if err != nil {
		t.Fatalf("scheduler setup: %v", err)
	}
	s.Jobs = sched
	go func() { _ = sched.Run(ctx) }()

	srv := httptest.NewServer(s.Routes())
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	do(t, client, http.MethodPost, srv.URL+"/api/setup", map[string]string{
		"email": "admin@test.local", "display_name": "Admin", "password": "test-password-1234",
	}, http.StatusOK)

	return srv, client, db, &calls
}

// allowWrites is the pair of decisions that stand between a discovered tool and
// a change: an administrator approves the tool, and an administrator enables
// writes for the plugin. Both are re-checked when a job fires.
func allowWrites(t *testing.T, srv *httptest.Server, client *http.Client) {
	t.Helper()
	do(t, client, http.MethodPost,
		srv.URL+"/api/capabilities/writer/thing.write/decide",
		map[string]string{"status": "approved"}, http.StatusOK)
	do(t, client, http.MethodPut,
		srv.URL+"/api/plugins/writer/writes",
		map[string]any{"enabled": true}, http.StatusOK)
}

// aCustomer makes one to schedule work against.
func aCustomer(t *testing.T, srv *httptest.Server, client *http.Client) string {
	t.Helper()
	made := do(t, client, http.MethodPost, srv.URL+"/api/customers",
		map[string]string{"display_name": "Acme Dental"}, http.StatusCreated)
	id, _ := made["id"].(string)
	if id == "" {
		t.Fatal("no customer was created")
	}
	return id
}

// waitFor polls until the condition holds or the test gives up. Scheduling is
// asynchronous by definition, so there is nothing to synchronise on.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// jobStatus reads one job back off the list.
func jobStatus(t *testing.T, srv *httptest.Server, client *http.Client, id string) (string, string) {
	t.Helper()
	list := do(t, client, http.MethodGet, srv.URL+"/api/jobs", nil, http.StatusOK)
	raw, _ := json.Marshal(list["jobs"])
	var jobs []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Result string `json:"result"`
	}
	_ = json.Unmarshal(raw, &jobs)
	for _, j := range jobs {
		if j.ID == id {
			return j.Status, j.Result
		}
	}
	return "", ""
}

/*
The whole path, once.

Scheduled through the API, held by JetStream, fired without anybody present,
and the plugin sees exactly one call. One is the number that matters: a firing
delivered twice would be a second change nobody asked for and nothing records.
*/
func TestScheduledWorkRunsOnceWithNobodyPresent(t *testing.T) {
	srv, client, _, calls := scheduled(t)
	allowWrites(t, srv, client)
	customer := aCustomer(t, srv, client)

	created := do(t, client, http.MethodPost, srv.URL+"/api/jobs", map[string]any{
		"plugin": "writer", "tool": "thing.write",
		"args":        map[string]any{"department": "Fort Worth"},
		"title":       "Back to normal hours",
		"customer_id": customer,
		"run_at":      time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339Nano),
	}, http.StatusCreated)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("the job came back without an id")
	}
	if calls.Load() != 0 {
		t.Fatal("scheduling something ran it immediately")
	}

	waitFor(t, "the job to run", func() bool {
		status, _ := jobStatus(t, srv, client, id)
		return status == "done"
	})
	if n := calls.Load(); n != 1 {
		t.Errorf("the plugin was called %d times, want exactly 1", n)
	}

	// And it stays run: nothing re-fires it afterwards.
	time.Sleep(2 * time.Second)
	if n := calls.Load(); n != 1 {
		t.Errorf("the plugin was called %d times after the job finished, want 1", n)
	}
}

/*
Cancelling actually disarms it.

The row saying cancelled is not the point; a timer still sitting in the stream
would fire regardless of what the row says. So this waits past the moment and
checks the far end never heard from us.
*/
func TestCancellingAJobStopsItHappening(t *testing.T) {
	srv, client, _, calls := scheduled(t)
	allowWrites(t, srv, client)
	customer := aCustomer(t, srv, client)

	created := do(t, client, http.MethodPost, srv.URL+"/api/jobs", map[string]any{
		"plugin": "writer", "tool": "thing.write",
		"title": "Close early", "customer_id": customer,
		"run_at": time.Now().Add(3 * time.Second).UTC().Format(time.RFC3339Nano),
	}, http.StatusCreated)
	id, _ := created["id"].(string)

	do(t, client, http.MethodDelete, srv.URL+"/api/jobs/"+id, nil, http.StatusOK)

	// Well past when it would have fired.
	time.Sleep(6 * time.Second)
	if n := calls.Load(); n != 0 {
		t.Errorf("a cancelled job called the plugin %d times", n)
	}
	if status, _ := jobStatus(t, srv, client, id); status != "cancelled" {
		t.Errorf("the job reads %q, want cancelled", status)
	}
}

/*
The approval is inherited, but the kill switches still work.

This is the whole security argument for deferred writes. Creating the schedule
was the approval — nothing asks a second time, because there is nobody there to
ask. What is asked again is everything an administrator can withdraw. Here they
withdraw the plugin's write switch between the arming and the moment, and the
job fires into a refusal instead of into somebody's phone system.
*/
func TestTurningWritesOffStopsWorkAlreadyScheduled(t *testing.T) {
	srv, client, _, calls := scheduled(t)
	allowWrites(t, srv, client)
	customer := aCustomer(t, srv, client)

	created := do(t, client, http.MethodPost, srv.URL+"/api/jobs", map[string]any{
		"plugin": "writer", "tool": "thing.write",
		"title": "Swap the routing device", "customer_id": customer,
		"run_at": time.Now().Add(3 * time.Second).UTC().Format(time.RFC3339Nano),
	}, http.StatusCreated)
	id, _ := created["id"].(string)

	// An administrator changes their mind, knowing nothing about this job.
	do(t, client, http.MethodPut, srv.URL+"/api/plugins/writer/writes",
		map[string]any{"enabled": false}, http.StatusOK)

	waitFor(t, "the job to be refused", func() bool {
		status, _ := jobStatus(t, srv, client, id)
		return status == "failed"
	})
	if n := calls.Load(); n != 0 {
		t.Errorf("the plugin was called %d times after writes were turned off", n)
	}
	if _, result := jobStatus(t, srv, client, id); result == "" {
		t.Error("nothing says why it did not run")
	}
}

/*
Withdrawing a tool's approval stops it too.

The other kill switch, and the one an administrator reaches for when a plugin
is misbehaving. Neither of these knows that scheduled work exists, which is the
point: they are checked at the moment of the change, so they cover changes
armed before anyone thought to worry.
*/
func TestWithdrawingApprovalStopsWorkAlreadyScheduled(t *testing.T) {
	srv, client, _, calls := scheduled(t)
	allowWrites(t, srv, client)
	customer := aCustomer(t, srv, client)

	created := do(t, client, http.MethodPost, srv.URL+"/api/jobs", map[string]any{
		"plugin": "writer", "tool": "thing.write",
		"title": "Raise everyone's role", "customer_id": customer,
		"run_at": time.Now().Add(3 * time.Second).UTC().Format(time.RFC3339Nano),
	}, http.StatusCreated)
	id, _ := created["id"].(string)

	do(t, client, http.MethodPost,
		srv.URL+"/api/capabilities/writer/thing.write/decide",
		map[string]string{"status": "rejected"}, http.StatusOK)

	waitFor(t, "the job to be refused", func() bool {
		status, _ := jobStatus(t, srv, client, id)
		return status == "failed"
	})
	if n := calls.Load(); n != 0 {
		t.Errorf("the plugin was called %d times after approval was withdrawn", n)
	}
}

// A tool nobody has approved cannot be scheduled either. The refusal belongs
// on the screen, in front of the person who can do something about it, rather
// than in a log at four in the morning.
func TestSchedulingAnUnapprovedToolIsRefused(t *testing.T) {
	srv, client, _, calls := scheduled(t)
	customer := aCustomer(t, srv, client)

	do(t, client, http.MethodPost, srv.URL+"/api/jobs", map[string]any{
		"plugin": "writer", "tool": "thing.write",
		"title": "Something nobody approved", "customer_id": customer,
		"run_at": time.Now().Add(3 * time.Second).UTC().Format(time.RFC3339Nano),
	}, http.StatusForbidden)

	time.Sleep(4 * time.Second)
	if n := calls.Load(); n != 0 {
		t.Errorf("a refused job called the plugin %d times", n)
	}
}

// A time that has already gone is refused rather than fired immediately. "It
// ran the moment I saved it" is never what somebody scheduling work meant.
func TestATimeThatHasPassedIsRefused(t *testing.T) {
	srv, client, _, _ := scheduled(t)
	allowWrites(t, srv, client)
	customer := aCustomer(t, srv, client)

	do(t, client, http.MethodPost, srv.URL+"/api/jobs", map[string]any{
		"plugin": "writer", "tool": "thing.write",
		"title": "Yesterday", "customer_id": customer,
		"run_at": time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano),
	}, http.StatusBadRequest)
}

// Reads are not schedulable. A scheduled read would make somebody's phone
// system answer questions in the middle of the night, to nobody.
func TestOnlyChangesCanBeScheduled(t *testing.T) {
	srv, client, _, _ := scheduled(t)
	allowWrites(t, srv, client)

	do(t, client, http.MethodPost, srv.URL+"/api/jobs", map[string]any{
		"plugin": "writer", "tool": "thing.read",
		"run_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
	}, http.StatusBadRequest)
}
