package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/store"
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
