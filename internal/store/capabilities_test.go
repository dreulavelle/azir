package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/dreulavelle/azir/internal/store"
)

/*
A plugin that goes away must not leave its approvals behind for the next thing
to claim its name.

plugins/ is a drop-in folder: retiring "web" and later dropping in an unrelated
binary called "web" would, without this, hand the newcomer every decision the
old one had earned. Approval is the whole of what makes a discovered tool safe
to call, so it has to expire with the thing it was granted to.
*/
func TestRetiredToolsAreForgotten(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := db.Observe(ctx, "web", "search", []string{"web.search"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Decide(ctx, "web", "search", store.StatusApproved, "admin@azir.local"); err != nil {
		t.Fatal(err)
	}
	if err := db.Observe(ctx, "searxng", "search", []string{"web.search"}); err != nil {
		t.Fatal(err)
	}

	// The renamed plugin stopped being discovered five weeks ago.
	age(t, db, "web", "search", 35*24*time.Hour)

	n, err := db.SweepCapabilities(ctx, store.RetiredAfter)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("forgot %d records, want the 1 nothing has offered in a month", n)
	}

	left := map[string]bool{}
	records, err := db.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		left[r.Plugin+"."+r.Tool] = true
	}
	if left["web.search"] {
		t.Error("a retired plugin's approval is still on the books to be inherited")
	}
	if !left["searxng.search"] {
		t.Error("the plugin that is still running lost its decision")
	}
}

// Forgetting a rejection points the wrong way: a tool an administrator refused
// would come back merely undecided.
func TestRejectionsAreKeptForever(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := db.Observe(ctx, "gone", "dangerous", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Decide(ctx, "gone", "dangerous", store.StatusRejected, "admin@azir.local"); err != nil {
		t.Fatal(err)
	}
	age(t, db, "gone", "dangerous", 400*24*time.Hour)

	if _, err := db.SweepCapabilities(ctx, store.RetiredAfter); err != nil {
		t.Fatal(err)
	}

	records, err := db.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if r.Plugin == "gone" && r.Status == store.StatusRejected {
			return
		}
	}
	t.Error("a refusal was forgotten; the tool would return as undecided")
}

// A plugin that is merely switched off keeps what it was granted. Losing an
// approval on every restart would train an administrator to approve without
// reading, which is the opposite of what the gate is for.
func TestARestartDoesNotCostApprovals(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := db.Observe(ctx, "3cx", "extensions.list", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Decide(ctx, "3cx", "extensions.list", store.StatusApproved, "admin@azir.local"); err != nil {
		t.Fatal(err)
	}
	age(t, db, "3cx", "extensions.list", 3*24*time.Hour)

	n, err := db.SweepCapabilities(ctx, store.RetiredAfter)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("forgot %d records; three days off is a restart, not a retirement", n)
	}
}

// age backdates when discovery last saw a tool, standing in for time passing.
func age(t *testing.T, db *store.DB, plugin, tool string, by time.Duration) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`UPDATE capabilities SET last_seen_at = now() - $3::interval
		 WHERE plugin = $1 AND tool = $2`,
		plugin, tool, by)
	if err != nil {
		t.Fatal(err)
	}
}
