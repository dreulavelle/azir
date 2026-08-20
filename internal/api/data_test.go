package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/internal/testsupport"
	"github.com/dreulavelle/azir/pkg/plugin"
)

// counts reads the data screen back as name to number.
func counts(t *testing.T, c *http.Client, url string) map[string]float64 {
	t.Helper()
	out := do(t, c, http.MethodGet, url+"/api/data", nil, http.StatusOK)
	got := map[string]float64{}
	stored, ok := out["stored"].([]any)
	if !ok {
		t.Fatalf("data read back as %v", out)
	}
	for _, entry := range stored {
		row, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, _ := row["name"].(string)
		n, _ := row["count"].(float64)
		got[name] = n
	}
	return got
}

/*
The promise the whole screen makes.

Starting fresh has to leave somebody signed in, still connected, and still
knowing who their customers are — an empty desk rather than a new install. If
clearing took the accounts or the credentials with it, the honest name for the
button would be "reinstall", and nobody would press it on a Tuesday afternoon
to get a clean slate for one piece of work.
*/
func TestClearingKeepsTheSetupAndRemovesTheWork(t *testing.T) {
	srv, client, db := serverWithDB(t)
	ctx := context.Background()

	me := do(t, client, http.MethodGet, srv.URL+"/api/me", nil, http.StatusOK)
	userID, err := uuid.Parse(me["user_id"].(string))
	if err != nil {
		t.Fatal(err)
	}

	customer, err := db.CreateCustomer(ctx, "Rainwater Plumbing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateConversation(ctx, userID, "Phones are dead", "", "", "test-model"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddSnapshot(ctx, store.Snapshot{
		CustomerID: customer.ID,
		Filename:   "support.zip",
		Report:     json.RawMessage(`{"health":"warning"}`),
	}); err != nil {
		t.Fatal(err)
	}

	before := counts(t, client, srv.URL)
	for _, name := range []string{"Conversations", "Diagnostic captures", "Customers", "People"} {
		if before[name] == 0 {
			t.Fatalf("%s was already empty; the test proves nothing", name)
		}
	}

	do(t, client, http.MethodPost, srv.URL+"/api/data/clear",
		map[string]string{"confirm": "clear"}, http.StatusOK)

	after := counts(t, client, srv.URL)
	for _, name := range []string{"Conversations", "Diagnostic captures"} {
		if after[name] != 0 {
			t.Errorf("%s survived clearing: %v left", name, after[name])
		}
	}
	if after["Customers"] != before["Customers"] {
		t.Errorf("customers went from %v to %v; the spine is setup, not work",
			before["Customers"], after["Customers"])
	}
	if after["People"] != before["People"] {
		t.Errorf("accounts went from %v to %v; clearing must not lock anybody out",
			before["People"], after["People"])
	}

	// The request above already proves it, since it was authenticated — but
	// say so, because "you are still signed in afterwards" is the part
	// somebody would notice being wrong.
	do(t, client, http.MethodGet, srv.URL+"/api/me", nil, http.StatusOK)
}

/*
A second click is not a confirmation.

Anything that only needs one more press is something people learn to press. The
word is typed, the server is what checks it, and a request without it is
refused — the console's button is not the only thing that can call this.
*/
func TestClearingNeedsTheWordTyped(t *testing.T) {
	srv, client, db := serverWithDB(t)

	if _, err := db.CreateCustomer(context.Background(), "Still Here Ltd"); err != nil {
		t.Fatal(err)
	}
	before := counts(t, client, srv.URL)

	for _, confirm := range []map[string]string{
		{},
		{"confirm": ""},
		{"confirm": "yes"},
		{"confirm": "clear everything"},
	} {
		do(t, client, http.MethodPost, srv.URL+"/api/data/clear", confirm, http.StatusBadRequest)
	}

	if after := counts(t, client, srv.URL); after["Customers"] != before["Customers"] {
		t.Errorf("a refused clear removed something anyway: %v then %v",
			before["Customers"], after["Customers"])
	}
}

// The log is emptied by the clearing, so the clearing has to be what is left in
// it. Recording that before the delete would have deleted the record.
func TestTheClearingIsTheEntryLeftInTheLog(t *testing.T) {
	srv, client, db := serverWithDB(t)
	ctx := context.Background()

	do(t, client, http.MethodPost, srv.URL+"/api/data/clear",
		map[string]string{"confirm": "clear"}, http.StatusOK)

	// Audit rows arrive over JetStream, so the one that must be there is
	// waited for rather than assumed to have landed already.
	for range 100 {
		var n int
		if err := db.Pool().QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE action = 'data.clear'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("the log does not say who emptied it")
}

/*
The deeper reset takes the setup and stops at the people.

Its whole reason for existing is the line it stops at. If it took the accounts
too it would be a reinstall, and the console would come back asking to be set
up rather than coming back empty — so the thing worth asserting is not what it
removed but that somebody is still able to use what is left.
*/
func TestResettingTakesTheSetupAndLeavesThePeople(t *testing.T) {
	srv, client, db := serverWithDB(t)
	ctx := context.Background()

	customer, err := db.CreateCustomer(ctx, "Rainwater Plumbing")
	if err != nil {
		t.Fatal(err)
	}
	creds := store.NewCredentials(db, testsupport.Vault(t), nil)
	if _, err := creds.Put(ctx, &customer.ID, "syncro", "api-key", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	me := do(t, client, http.MethodGet, srv.URL+"/api/me", nil, http.StatusOK)
	userID, err := uuid.Parse(me["user_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateConversation(ctx, userID, "Phones are dead", "", "", "test-model"); err != nil {
		t.Fatal(err)
	}

	before := counts(t, client, srv.URL)
	for _, name := range []string{"Customers", "Connections", "Conversations", "People"} {
		if before[name] == 0 {
			t.Fatalf("%s was already empty; the test proves nothing", name)
		}
	}

	do(t, client, http.MethodPost, srv.URL+"/api/data/reset",
		map[string]string{"confirm": "reset"}, http.StatusOK)

	after := counts(t, client, srv.URL)
	for _, name := range []string{"Customers", "Connections", "Conversations"} {
		if after[name] != 0 {
			t.Errorf("%s survived the reset: %v left", name, after[name])
		}
	}
	if after["People"] != before["People"] {
		t.Errorf("accounts went from %v to %v; the reset must stop at the people",
			before["People"], after["People"])
	}
	// The request above was authenticated, but say it plainly: coming back
	// still signed in is the difference between a reset and a reinstall.
	do(t, client, http.MethodGet, srv.URL+"/api/me", nil, http.StatusOK)
}

/*
The safe word does not work on the dangerous button.

Both live on one screen and Start fresh is the one somebody will have pressed
before, so a shared confirmation would turn the habit of the smaller action
into the habit of the larger one.
*/
func TestResettingNeedsItsOwnWord(t *testing.T) {
	srv, client, db := serverWithDB(t)

	if _, err := db.CreateCustomer(context.Background(), "Still Here Ltd"); err != nil {
		t.Fatal(err)
	}
	before := counts(t, client, srv.URL)

	for _, confirm := range []map[string]string{
		{}, {"confirm": ""}, {"confirm": "clear"}, {"confirm": "yes"},
	} {
		do(t, client, http.MethodPost, srv.URL+"/api/data/reset", confirm, http.StatusBadRequest)
	}

	if after := counts(t, client, srv.URL); after["Customers"] != before["Customers"] {
		t.Errorf("a refused reset removed something anyway: %v then %v",
			before["Customers"], after["Customers"])
	}
}

/*
A reset that nobody could come back from is refused, not confirmed.

The reset removes single sign-on. An account that has only ever arrived through
the provider has no password to fall back on, so if every account is like that
the reset is a lockout with no way back in from a browser. No dialog makes that
a reasonable thing to let somebody do to themselves, so the server declines it
even though the word was typed correctly.
*/
func TestResettingIsRefusedWhenNobodyCouldSignInAfter(t *testing.T) {
	srv, client, db := serverWithDB(t)
	ctx := context.Background()

	if _, err := db.CreateCustomer(ctx, "Still Here Ltd"); err != nil {
		t.Fatal(err)
	}
	before := counts(t, client, srv.URL)

	// Everyone federated, nobody with a password.
	if _, err := db.Pool().Exec(ctx,
		`UPDATE users SET password_hash = NULL, provider = 'oidc'`); err != nil {
		t.Fatal(err)
	}

	do(t, client, http.MethodPost, srv.URL+"/api/data/reset",
		map[string]string{"confirm": "reset"}, http.StatusConflict)

	if after := counts(t, client, srv.URL); after["Customers"] != before["Customers"] {
		t.Errorf("the refused reset removed something anyway: %v then %v",
			before["Customers"], after["Customers"])
	}

	// One password is enough to make it allowed again.
	hash, err := identity.HashPassword("a-real-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx,
		`UPDATE users SET password_hash = $1 WHERE email = (SELECT min(email) FROM users)`,
		hash); err != nil {
		t.Fatal(err)
	}
	do(t, client, http.MethodPost, srv.URL+"/api/data/reset",
		map[string]string{"confirm": "reset"}, http.StatusOK)
}

/*
The number on the screen is the number that is enforced.

Both retention periods were constants compiled into the binary while the Data
screen displayed them as a promise about somebody's customers' data — so an MSP
that had agreed to hold captures for thirty days could keep that promise only
by rebuilding Azir. What matters is not that the setting saves, but that the
label moves with it: two places claiming different numbers is worse than one
that cannot be changed.
*/
func TestRetentionIsWhatTheScreenSays(t *testing.T) {
	srv, client := server(t)

	expiry := func() map[string]string {
		out := do(t, client, http.MethodGet, srv.URL+"/api/data", nil, http.StatusOK)
		said := map[string]string{}
		for _, entry := range out["stored"].([]any) {
			row := entry.(map[string]any)
			if words, ok := row["expires"].(string); ok {
				said[row["name"].(string)] = words
			}
		}
		return said
	}

	if got := expiry()["Diagnostic captures"]; got != "14 days, unless pinned" {
		t.Fatalf("shipped default reads %q", got)
	}

	do(t, client, http.MethodPut, srv.URL+"/api/data/retention",
		map[string]int{"capture_days": 30, "cache_days": 2}, http.StatusOK)

	said := expiry()
	if said["Diagnostic captures"] != "30 days, unless pinned" {
		t.Errorf("captures still read %q after the setting changed", said["Diagnostic captures"])
	}
	if said["Cached answers"] != "2 days" {
		t.Errorf("cached answers still read %q", said["Cached answers"])
	}
}

// Out of bounds is refused with a sentence, not a database error — and nothing
// is saved, so the screen keeps saying what is still true.
func TestRetentionRefusesNonsense(t *testing.T) {
	srv, client := server(t)

	for _, bad := range []map[string]int{
		{"capture_days": 0, "cache_days": 7},
		{"capture_days": 14, "cache_days": 0},
		{"capture_days": 4000, "cache_days": 7},
		{"capture_days": -1, "cache_days": 7},
	} {
		do(t, client, http.MethodPut, srv.URL+"/api/data/retention", bad, http.StatusBadRequest)
	}

	out := do(t, client, http.MethodGet, srv.URL+"/api/data", nil, http.StatusOK)
	keeping := out["retention"].(map[string]any)
	if keeping["capture_days"] != float64(14) || keeping["cache_days"] != float64(7) {
		t.Errorf("a refused change was saved anyway: %v", keeping)
	}
}

/*
A capture stored after the setting changes expires on the new clock.

The label moving is the visible half; this is the half that matters. It was a
constant read at the moment a capture was written, so changing the setting and
having it apply only to a restarted process would be the obvious way to get
this wrong.
*/
func TestACaptureExpiresOnTheConfiguredClock(t *testing.T) {
	srv, client, db := serverWithDB(t)
	ctx := context.Background()

	do(t, client, http.MethodPut, srv.URL+"/api/data/retention",
		map[string]int{"capture_days": 30, "cache_days": 7}, http.StatusOK)

	customer, err := db.CreateCustomer(ctx, "Rainwater Plumbing")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := db.AddSnapshot(ctx, store.Snapshot{
		CustomerID: customer.ID,
		Filename:   "support.zip",
		Report:     json.RawMessage(`{"health":"ok"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ExpiresAt == nil {
		t.Fatal("a capture with no expiry is a capture that never goes away")
	}

	days := time.Until(*saved.ExpiresAt).Hours() / 24
	if days < 29 || days > 31 {
		t.Errorf("expires in %.1f days, wanted about 30", days)
	}
}

/*
Resetting must tell every plugin, not just the database.

This is the loudest version of a gap found four times now: the rows go, the
console says so, and each plugin carries on serving customers from a credential
it cached five minutes ago. "Start fresh" leaving the deployment still connected
to somebody's phone system is the worst possible reading of that button.

Both plugins in the harness are watched, because a reset is not about any one of
them — announcing to the first and stopping would look identical from a test
that only listened to one.
*/
func TestResettingTellsEveryPlugin(t *testing.T) {
	srv, client, _, nc := serverWithBus(t)

	heard := make(chan string, 8)
	for _, name := range []string{"writer", "reader"} {
		sub, err := nc.Subscribe(plugin.ConfigChangedSubject(name), func(m *nats.Msg) {
			select {
			case heard <- m.Subject:
			default:
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Unsubscribe() //nolint:errcheck // test cleanup
	}

	do(t, client, http.MethodPost, srv.URL+"/api/data/reset",
		map[string]string{"confirm": "reset"}, http.StatusOK)

	seen := map[string]bool{}
	deadline := time.After(4 * time.Second)
	for len(seen) < 2 {
		select {
		case subject := <-heard:
			seen[subject] = true
		case <-deadline:
			t.Fatalf("a reset announced itself to %d of 2 plugins; the rest keep serving from cache", len(seen))
		}
	}
}
