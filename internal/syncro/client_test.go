package syncro_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dreulavelle/azir/internal/syncro"
	"github.com/dreulavelle/azir/pkg/plugin"
)

// fakeSyncro stands in for the vendor. Responses use the field names Syncro
// actually sends, so the trimming is tested against the real shape rather than
// against our own idea of it.
func fakeSyncro(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *syncro.Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// New() enforces https and builds the hostname from the subdomain, so it
	// cannot point at a test server. Reaching through plugin.NewHTTPClient
	// directly is the honest way to test the trimming and paging logic; the
	// URL construction is covered separately below.
	client, err := syncro.NewForTest(srv.URL, func(_ context.Context) (string, error) {
		return "test-api-token", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, client
}

func TestListCustomersTrimsResponse(t *testing.T) {
	_, client := fakeSyncro(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-token" {
			t.Errorf("unexpected auth header %q", got)
		}
		// The credential must never travel in the query string.
		if r.URL.Query().Get("api_key") != "" {
			t.Error("credential was sent as a query parameter")
		}
		_, _ = w.Write([]byte(`{
			"customers": [{
				"id": 42,
				"firstname": "Dana",
				"lastname": "Scully",
				"business_name": "Acme Dental",
				"email": "dana@acme.example",
				"mobile": "555-0100",
				"address": "1 Main St", "city": "Springfield", "state": "IL", "zip": "62701",
				"notes": "prefers phone contact",
				"created_at": "2026-01-05T10:00:00Z",
				"internal_only_field": "should not survive"
			}],
			"meta": {"total_pages": 3, "total_entries": 61, "page": 1}
		}`))
	})

	got, err := client.ListCustomers(context.Background(), "acme", 1, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("want 1 customer, got %d", len(got.Items))
	}

	c := got.Items[0]
	if c.Name != "Dana Scully" {
		t.Errorf("name not composed from parts: %q", c.Name)
	}
	if c.Phone != "555-0100" {
		t.Errorf("mobile was not used when phone is absent: %q", c.Phone)
	}
	if c.Address != "1 Main St, Springfield, IL, 62701" {
		t.Errorf("address not composed: %q", c.Address)
	}
	if got.Page.TotalCount != 61 || got.Page.TotalPages != 3 {
		t.Errorf("pagination not carried through: %+v", got.Page)
	}

	// Trimming is the point: a vendor field we did not ask for must not reach
	// model context.
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "internal_only_field") {
		t.Errorf("an untrimmed vendor field survived: %s", raw)
	}
}

// A search must not return every comment on every hit — that is how a context
// window gets swamped by one call.
func TestTicketSearchOmitsComments(t *testing.T) {
	_, client := fakeSyncro(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"tickets": [{
				"id": 7, "number": 1042, "subject": "Printer offline",
				"status": "New", "priority": "Normal",
				"customer": {"business_name": "Acme Dental"},
				"user": {"full_name": "Fox Mulder"},
				"comments": [{"id": 1, "body": "a very long thread"}]
			}],
			"meta": {"total_pages": 1, "total_entries": 1}
		}`))
	})

	got, err := client.SearchTickets(context.Background(), syncro.TicketSearch{Query: "printer"})
	if err != nil {
		t.Fatal(err)
	}
	ticket := got.Items[0]

	if len(ticket.Comments) != 0 {
		t.Error("search returned comment threads; that is what tickets.get is for")
	}
	if ticket.Number != "1042" {
		t.Errorf("numeric ticket number not stringified: %q", ticket.Number)
	}
	if ticket.Customer != "Acme Dental" || ticket.AssignedTo != "Fox Mulder" {
		t.Errorf("nested fields not flattened: %+v", ticket)
	}
}

func TestGetTicketIncludesComments(t *testing.T) {
	_, client := fakeSyncro(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/tickets/7") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"ticket": {
				"id": 7, "number": "T-1042", "subject": "Printer offline",
				"comments": [
					{"id": 1, "body": "reported by customer", "tech": "Dana", "hidden": false},
					{"id": 2, "body": "internal note", "hidden": true}
				]
			}
		}`))
	})

	got, err := client.GetTicket(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Comments) != 2 {
		t.Fatalf("want 2 comments, got %d", len(got.Comments))
	}
	// Hidden comments are internal notes and are genuinely useful context, but
	// the flag must survive so a draft never quotes one back to a customer.
	if !got.Comments[1].Hidden {
		t.Error("the hidden flag was lost; an internal note could be quoted to a customer")
	}
}

// Per-page is capped so one call cannot pull an unbounded page into context.
func TestPerPageIsCapped(t *testing.T) {
	var seen atomic.Value
	_, client := fakeSyncro(t, func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.URL.Query().Get("per_page"))
		_, _ = w.Write([]byte(`{"customers":[],"meta":{}}`))
	})

	if _, err := client.ListCustomers(context.Background(), "", 1, 10000); err != nil {
		t.Fatal(err)
	}
	if got := seen.Load().(string); got != "100" {
		t.Errorf("per_page was not capped: %q", got)
	}
}

// A bad credential must surface to a human rather than being retried into an
// IP ban, and the message must say where to fix it.
func TestUnauthorizedIsActionable(t *testing.T) {
	var calls atomic.Int32
	_, client := fakeSyncro(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	})

	_, err := client.ListCustomers(context.Background(), "", 1, 25)
	if err == nil {
		t.Fatal("expected an error")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("auth failure retried %d times", n-1)
	}

	var perr *plugin.Error
	if !errors.As(err, &perr) || !strings.Contains(perr.Message, "settings") {
		t.Errorf("error is not actionable: %v", err)
	}
}

// Malformed vendor JSON must not propagate its content to the caller.
func TestUnreadableResponseIsSanitised(t *testing.T) {
	_, client := fakeSyncro(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"customers": "this should be an array, token=SUPERSECRET"}`))
	})

	_, err := client.ListCustomers(context.Background(), "", 1, 25)
	if err == nil {
		t.Fatal("expected a decode failure")
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Errorf("vendor response content reached the caller: %v", err)
	}
}

// The subdomain becomes a hostname label, so it cannot be interpolated blindly.
func TestSubdomainIsValidated(t *testing.T) {
	credential := func(context.Context) (string, error) { return "token", nil }

	for _, bad := range []string{
		"", "acme.evil.example", "acme/../../x", "acme:8080",
		"-leading", "trailing-", "UPPER OK but spaces not",
		strings.Repeat("a", 64),
	} {
		if _, err := syncro.New(bad, credential); err == nil {
			t.Errorf("accepted an unsafe subdomain: %q", bad)
		}
	}

	for _, good := range []string{"acme", "acme-dental", "a1", "ACME"} {
		if _, err := syncro.New(good, credential); err != nil {
			t.Errorf("rejected a valid subdomain %q: %v", good, err)
		}
	}
}

// Every method on this client is a read. Nothing may be able to write.
func TestClientCannotWrite(t *testing.T) {
	var methods atomic.Value
	methods.Store([]string{})

	srv, _ := fakeSyncro(t, func(w http.ResponseWriter, r *http.Request) {
		got := methods.Load().([]string)
		methods.Store(append(got, r.Method))
		_, _ = w.Write([]byte(`{"customers":[],"tickets":[],"assets":[],"meta":{}}`))
	})
	_ = srv

	client, err := syncro.NewForTest(srv.URL, func(context.Context) (string, error) {
		return "token", nil
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	_, _ = client.ListCustomers(ctx, "", 1, 5)
	_, _ = client.SearchTickets(ctx, syncro.TicketSearch{})
	_, _ = client.ListAssets(ctx, 0, 1, 5)
	_, _ = client.GetTicket(ctx, 1)
	_, _ = client.GetCustomer(ctx, 1)

	for _, m := range methods.Load().([]string) {
		if m != http.MethodGet {
			t.Errorf("client issued a %s; every Syncro operation must be a read", m)
		}
	}
}
