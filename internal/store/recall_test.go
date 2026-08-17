package store_test

import (
	"context"
	"testing"

	"github.com/dreulavelle/azir/internal/store"
)

func remember(t *testing.T, db *store.DB, id, subject, problem, resolution string) {
	t.Helper()
	if err := db.RememberTicket(context.Background(), store.Remembered{
		Source: "work_items", ExternalID: id, Subject: subject,
		Problem: problem, Resolution: resolution,
	}); err != nil {
		t.Fatal(err)
	}
}

// Somebody describing a fault in their own words has to reach a ticket that
// described it in different words. That is the whole reason this is not a
// LIKE query.
func TestRecallMatchesDifferentWording(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	remember(t, db, "1", "Printer keeps going offline",
		"The printer in reception drops off the network every afternoon",
		"Static IP was outside the DHCP reservation. Reserved .50 and it has been stable since.")
	remember(t, db, "2", "New starter setup", "Need an account for Jess", "Created the account.")

	found, err := db.Recall(ctx, store.RecallQuery{Text: "printer dropping off network"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 {
		t.Fatal("no match for a paraphrase of the subject")
	}
	if found[0].ExternalID != "1" {
		t.Errorf("best match was %s, want the printer ticket", found[0].ExternalID)
	}
}

// The other half of most searches is not prose at all: an error code, a model
// number, a fragment somebody half remembers. A stemmer is no help there.
func TestRecallMatchesAnErrorCode(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	remember(t, db, "10", "Outlook error 0x8004010F on two machines",
		"Two users cannot send", "Rebuilt the OST. Fixed.")
	remember(t, db, "11", "Slow laptop", "It is slow", "Added RAM.")

	found, err := db.Recall(ctx, store.RecallQuery{Text: "0x8004010F"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 || found[0].ExternalID != "10" {
		t.Errorf("an error code did not find its ticket: %+v", found)
	}
}

// A ticket nobody wrote a resolution on cannot tell anybody what fixed it, and
// offering it as an answer wastes the reader's time.
func TestRecallSkipsTicketsWithNoResolution(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	remember(t, db, "20", "VPN keeps disconnecting", "It drops every hour", "")
	found, err := db.Recall(ctx, store.RecallQuery{Text: "vpn disconnecting"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("returned a ticket with no resolution: %+v", found)
	}

	// Unless somebody asks for them, which is a different question.
	found, err = db.Recall(ctx, store.RecallQuery{Text: "vpn disconnecting", IncludeUnresolved: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Errorf("asked for unresolved and got %d", len(found))
	}
}

// The ticket being read must not be offered as its own precedent.
func TestRecallExcludesTheTicketYouAreOn(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	remember(t, db, "30", "Firewall blocking SIP", "Calls fail", "Opened 5060.")
	remember(t, db, "31", "Firewall blocking SIP again", "Calls fail", "Opened 5060 on the new box.")

	found, err := db.Recall(ctx, store.RecallQuery{Text: "firewall blocking SIP", Exclude: "30"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		if f.ExternalID == "30" {
			t.Error("a ticket was returned as its own precedent")
		}
	}
}

// Reindexing the same ticket updates it rather than making a second copy —
// every webhook reindexes the recent page, so this happens constantly.
func TestRememberingTwiceUpdates(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	remember(t, db, "40", "Mailbox full", "Cannot receive", "")
	remember(t, db, "40", "Mailbox full", "Cannot receive", "Archived 2019 and earlier.")

	total, resolved, err := db.RecallSize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("indexed the same ticket %d times", total)
	}
	if resolved != 1 {
		t.Error("the resolution added on the second pass was not kept")
	}
}

// Punctuation must not break a search. People paste error messages.
func TestRecallSurvivesPastedText(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	remember(t, db, "50", "Backup failing", "Veeam job fails", "Repository was full.")

	for _, query := range []string{
		`Error: backup "job" failed (code: 1603)`,
		"backup & restore",
		"why is the backup failing?",
	} {
		if _, err := db.Recall(ctx, store.RecallQuery{Text: query}); err != nil {
			t.Errorf("search failed on %q: %v", query, err)
		}
	}
}

// A backfill over years of tickets must survive a restart, or nobody ever
// completes one.
func TestBackfillProgressResumes(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	at, err := db.BackfillProgress(ctx, "work_items")
	if err != nil {
		t.Fatal(err)
	}
	if at.LastPage != 0 || at.Complete {
		t.Errorf("a fresh index should start at the beginning, got %+v", at)
	}

	if err := db.SetBackfillProgress(ctx, store.RecallProgress{
		Source: "work_items", LastPage: 7, Indexed: 700,
	}); err != nil {
		t.Fatal(err)
	}
	at, err = db.BackfillProgress(ctx, "work_items")
	if err != nil {
		t.Fatal(err)
	}
	if at.LastPage != 7 || at.Indexed != 700 {
		t.Errorf("progress did not survive: %+v", at)
	}
}

/*
One word that happens not to appear must not exclude everything.

"printer not printing" found nothing against a ticket titled "Cannot print to
the upstairs Xerox", because the terms were joined with AND and the subject
stems to print rather than printer. A fault description is a guess at wording;
requiring every guess to land requires the reporter to already know the answer.
*/
func TestRecallDoesNotRequireEveryWord(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	remember(t, db, "60", "Cannot print to the upstairs Xerox",
		"Nothing comes out", "Queue was stuck. Cleared the spooler.")

	for _, query := range []string{
		"printer not printing",
		"printing problem upstairs",
		"xerox jammed and offline",
	} {
		found, err := db.Recall(ctx, store.RecallQuery{Text: query})
		if err != nil {
			t.Fatalf("%q: %v", query, err)
		}
		if len(found) == 0 {
			t.Errorf("%q found nothing, though it shares words with the ticket", query)
		}
	}
}

// Matching more of the question still has to win, or loosening the query would
// just return everything in an arbitrary order.
func TestRecallRanksTheCloserMatchFirst(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	remember(t, db, "70", "Backup job failing on the file server",
		"Veeam reports a failure every night", "Repository was full. Pruned old restore points.")
	remember(t, db, "71", "Server room air conditioning",
		"It is warm in there", "Engineer attended.")

	found, err := db.Recall(ctx, store.RecallQuery{Text: "backup job failing server"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 || found[0].ExternalID != "70" {
		t.Errorf("the closer match did not come first: %+v", found)
	}
}
