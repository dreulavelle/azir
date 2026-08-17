package syncro_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dreulavelle/azir/internal/syncro"
)

func TestIsDone(t *testing.T) {
	// The statuses a Syncro account actually ships with, plus the ones an
	// account is likely to invent. "Incomplete" is the reason this reads with
	// word boundaries: it contains "complete" and means the opposite.
	cases := map[string]bool{
		"Resolved":             true,
		"resolved":             true,
		"Closed":               true,
		"Closed - No Response": true,
		"Completed":            true,
		"Done":                 true,
		"Cancelled":            true,
		"Canceled by customer": true,
		"New":                  false,
		"In Progress":          false,
		"Waiting on Customer":  false,
		"Waiting for Parts":    false,
		"Scheduled":            false,
		"Incomplete":           false,
		"Undone":               false,
		"Escalated":            false,
		"":                     false,
		"Customer Reply":       false,
		// Still work to do, and it says "closing" — the reason this matches on
		// whole words rather than anywhere in the string.
		"Needs closing summary": false,
	}
	for status, want := range cases {
		if got := syncro.IsDone(status); got != want {
			t.Errorf("IsDone(%q) = %v, want %v", status, got, want)
		}
	}
}

// tickets builds one page of Syncro's ticket list.
func ticketPage(page, totalPages int, statuses ...string) map[string]any {
	items := make([]map[string]any, 0, len(statuses))
	for i, s := range statuses {
		items = append(items, map[string]any{
			"id":      page*100 + i,
			"subject": fmt.Sprintf("ticket %d-%d", page, i),
			"status":  s,
		})
	}
	return map[string]any{
		"tickets": items,
		"meta": map[string]any{
			"page":          page,
			"total_pages":   totalPages,
			"total_entries": totalPages * len(statuses),
		},
	}
}

// An open-only search reads past the resolved tickets rather than handing back
// a short page. Filtering in the browser instead was the bug: a hundred rows
// fetched, most of them finished, and the old open ticket never reached.
func TestOpenOnlyReadsPastResolvedTickets(t *testing.T) {
	var pagesRead []int
	_, client := fakeSyncro(t, func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		pagesRead = append(pagesRead, page)

		// Page one is almost entirely finished work; the open ticket worth
		// finding is on page two.
		var body map[string]any
		switch page {
		case 1:
			body = ticketPage(1, 3, "Resolved", "Closed", "Resolved", "In Progress")
		case 2:
			body = ticketPage(2, 3, "Resolved", "Waiting on Customer", "New")
		default:
			body = ticketPage(3, 3, "Resolved")
		}
		_ = json.NewEncoder(w).Encode(body)
	})

	got, err := client.SearchTickets(context.Background(), syncro.TicketSearch{
		OpenOnly: true,
		PerPage:  3,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Items) != 3 {
		t.Fatalf("got %d open tickets, want 3: %+v", len(got.Items), got.Items)
	}
	for _, item := range got.Items {
		if syncro.IsDone(item.Status) {
			t.Errorf("a finished ticket survived the filter: %q", item.Status)
		}
	}
	// Two pages was enough; a third request would have been work for nothing.
	if len(pagesRead) != 2 {
		t.Errorf("read pages %v, want exactly pages 1 and 2", pagesRead)
	}
}

// Reaching the end of Syncro's list stops the scan. Without this it would keep
// asking for pages that do not exist until the scan bound ran out.
func TestOpenOnlyStopsAtTheEndOfTheList(t *testing.T) {
	requests := 0
	_, client := fakeSyncro(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(ticketPage(1, 1, "Resolved", "Closed"))
	})

	got, err := client.SearchTickets(context.Background(), syncro.TicketSearch{
		OpenOnly: true,
		PerPage:  50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 0 {
		t.Errorf("got %d tickets, want none — every one was finished", len(got.Items))
	}
	if requests != 1 {
		t.Errorf("made %d requests, want 1: the first page was the last page", requests)
	}
	// An empty result has to be an explicit empty list rather than null, or a
	// caller cannot tell "no open tickets" from "the field is missing".
	if got.Items == nil {
		t.Error("items came back null; an empty queue is a fact, not an absence")
	}
}

// Whatever a page yielded is kept whole. Trimming to the requested size would
// drop tickets that the next request — which asks for the page after this one —
// would never return.
func TestOpenOnlyKeepsEveryTicketItAlreadyRead(t *testing.T) {
	_, client := fakeSyncro(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ticketPage(1, 4, "New", "New", "New", "New"))
	})

	got, err := client.SearchTickets(context.Background(), syncro.TicketSearch{
		OpenOnly: true,
		PerPage:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 4 {
		t.Errorf("got %d tickets, want all 4 read from that page", len(got.Items))
	}
	if got.Page.Page != 1 || got.Page.TotalPages != 4 {
		t.Errorf("page reported as %+v, want Syncro's own position so a caller can ask for the next one", got.Page)
	}
}

// The scan is bounded. A queue of nothing but finished tickets must not turn
// one search into an unbounded walk of the vendor's entire history.
func TestOpenOnlyScanIsBounded(t *testing.T) {
	requests := 0
	_, client := fakeSyncro(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		_ = json.NewEncoder(w).Encode(ticketPage(page, 1000, "Resolved", "Closed"))
	})

	if _, err := client.SearchTickets(context.Background(), syncro.TicketSearch{
		OpenOnly: true,
		PerPage:  100,
	}); err != nil {
		t.Fatal(err)
	}
	if requests > 5 {
		t.Errorf("made %d requests; the scan is meant to stop at 5", requests)
	}
}

// A plain search still costs exactly one request, and still returns finished
// tickets — someone filtering by "Resolved" on purpose means it.
func TestSearchWithoutOpenOnlyIsUnchanged(t *testing.T) {
	requests := 0
	_, client := fakeSyncro(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(ticketPage(1, 9, "Resolved", "New"))
	})

	got, err := client.SearchTickets(context.Background(), syncro.TicketSearch{PerPage: 100})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Errorf("made %d requests, want 1", requests)
	}
	if len(got.Items) != 2 {
		t.Errorf("got %d tickets, want both — nothing asked for them to be filtered", len(got.Items))
	}
}

// The link back to Syncro is built from the account's own address. A client
// with no site to name leaves it off rather than guessing one.
func TestTicketLinkIsAbsentWithoutASite(t *testing.T) {
	_, client := fakeSyncro(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ticketPage(1, 1, "New"))
	})

	got, err := client.SearchTickets(context.Background(), syncro.TicketSearch{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Items[0].URL != "" {
		t.Errorf("got a link %q from a client that has no site", got.Items[0].URL)
	}
}

func TestTicketLinkPointsAtTheAccount(t *testing.T) {
	client, err := syncro.New("acme", func(context.Context) (string, error) { return "t", nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := syncro.LinkForTest(client, syncro.Ticket{ID: 4233}); got != "https://acme.syncromsp.com/tickets/4233" {
		t.Errorf("link = %q", got)
	}
}

// A comment means the same thing whichever endpoint returned it.
//
// The write path used to build the struct by hand, so posting an internal note
// came back hidden and simultaneously not internal, with nobody recorded as
// having said it — while reading that same note a second later gave the right
// answer for all three.
func TestPostedCommentIsTrimmedLikeAReadOne(t *testing.T) {
	_, client := fakeSyncro(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"comment": map[string]any{
					"id": 99, "body": "a private note", "hidden": true,
					"tech": "Dreu", "created_at": "2026-08-17T00:00:00Z",
				},
			})
			return
		}
		// The same comment, read back on the ticket.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ticket": map[string]any{
				"id": 1, "subject": "s",
				"comments": []map[string]any{{
					"id": 99, "body": "a private note", "hidden": true,
					"tech": "Dreu", "created_at": "2026-08-17T00:00:00Z",
				}},
			},
		})
	})

	posted, err := client.PostComment(context.Background(), syncro.CommentRequest{
		TicketID: 1, Body: "a private note", Hidden: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	read, err := client.GetTicket(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Comments) != 1 {
		t.Fatalf("expected the comment back on the ticket, got %d", len(read.Comments))
	}

	if posted != read.Comments[0] {
		t.Errorf("the same comment differs by endpoint:\n posted %+v\n read   %+v", posted, read.Comments[0])
	}
	if !posted.Internal || posted.From == "" {
		t.Errorf("a hidden comment came back internal=%v from=%q", posted.Internal, posted.From)
	}
}

// A status that this account does not define must be refused before it is sent.
//
// Syncro accepts any string, so a typo does not fail — it puts a ticket in a
// state no filter or report will ever surface again.
func TestUpdateRefusesAnInventedStatus(t *testing.T) {
	var wrote bool
	_, client := fakeSyncro(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			wrote = true
		}
		if strings.HasSuffix(r.URL.Path, "/tickets/settings") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ticket_status_list": []string{"New", "In Progress", "Resolved"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ticket": map[string]any{"id": 1}})
	})

	_, err := client.UpdateTicket(context.Background(), syncro.TicketUpdate{
		TicketID: 1, Status: "Nearly Done",
	})
	if err == nil {
		t.Fatal("an invented status was accepted")
	}
	if !strings.Contains(err.Error(), "In Progress") {
		t.Errorf("the refusal does not say what is valid: %v", err)
	}
	if wrote {
		t.Error("it sent the write anyway")
	}
}

// The account's own spelling wins, so "resolved" does not become a second
// status distinct from "Resolved".
func TestUpdateUsesTheAccountsCasing(t *testing.T) {
	var sent map[string]any
	_, client := fakeSyncro(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tickets/settings") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ticket_status_list": []string{"New", "Resolved"},
			})
			return
		}
		if r.Method == http.MethodPut {
			_ = json.NewDecoder(r.Body).Decode(&sent)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ticket": map[string]any{"id": 1}})
	})

	if _, err := client.UpdateTicket(context.Background(), syncro.TicketUpdate{
		TicketID: 1, Status: "resolved",
	}); err != nil {
		t.Fatal(err)
	}
	if sent["status"] != "Resolved" {
		t.Errorf("sent status %q, want the account's own casing", sent["status"])
	}
}

// Only the fields somebody set are sent, so changing a status cannot blank an
// assignee by omission.
func TestUpdateSendsOnlyWhatChanged(t *testing.T) {
	var sent map[string]any
	_, client := fakeSyncro(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tickets/settings") {
			_ = json.NewEncoder(w).Encode(map[string]any{"ticket_status_list": []string{"New"}})
			return
		}
		if r.Method == http.MethodPut {
			_ = json.NewDecoder(r.Body).Decode(&sent)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ticket": map[string]any{"id": 1}})
	})

	if _, err := client.UpdateTicket(context.Background(), syncro.TicketUpdate{
		TicketID: 1, UserID: 7,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := sent["status"]; ok {
		t.Errorf("an unset status was sent anyway: %v", sent)
	}
	if sent["user_id"] != float64(7) {
		t.Errorf("user_id = %v, want 7", sent["user_id"])
	}
}

// Nothing to change is a mistake worth naming rather than an empty write.
func TestUpdateWithNothingSetIsRefused(t *testing.T) {
	_, client := fakeSyncro(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ticket": map[string]any{"id": 1}})
	})
	if _, err := client.UpdateTicket(context.Background(), syncro.TicketUpdate{TicketID: 1}); err == nil {
		t.Error("an update with no fields was accepted")
	}
}
