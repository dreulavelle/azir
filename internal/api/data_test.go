package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/internal/testsupport"
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
	creds := store.NewCredentials(db, testsupport.Vault(t))
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
