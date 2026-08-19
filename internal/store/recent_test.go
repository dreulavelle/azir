package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/store"
)

/*
Where somebody was last.

The picker shows this before anything is typed, in place of the first twenty
customers by name — so it has to be the handful they were actually working on,
in the order they last touched them, and nobody else's.
*/
func TestRecentlyChanged(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	// Six customers, changed at known times, so the order and the cut are both
	// checkable rather than incidental.
	names := []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot"}
	ids := make([]uuid.UUID, len(names))
	for i, name := range names {
		c, err := db.CreateCustomer(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = c.ID
	}

	base := time.Now().Add(-24 * time.Hour)
	change := func(at time.Time, actor string, customer uuid.UUID, action, outcome string) {
		t.Helper()
		_, err := db.Pool().Exec(ctx, `
			INSERT INTO audit_log (occurred_at, actor_user_id, action, customer_id, outcome)
			VALUES ($1, $2, $3, $4, $5)`, at, actor, action, customer, outcome)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Alpha oldest, Foxtrot newest.
	for i, id := range ids {
		change(base.Add(time.Duration(i)*time.Hour), "ann@example.com", id, "extension.change", "ok")
	}
	// Touched twice: the later one is what places it, so Alpha jumps the queue.
	change(base.Add(9*time.Hour), "ann@example.com", ids[0], "bulk.apply", "ok")

	got, err := db.RecentlyChanged(ctx, "ann@example.com", 5)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"Alpha", "Foxtrot", "Echo", "Delta", "Charlie"}
	if len(got) != len(want) {
		t.Fatalf("got %d customers, want %d: %v", len(got), len(want), display(got))
	}
	for i, name := range want {
		if got[i].DisplayName != name {
			t.Errorf("position %d is %s, want %s (whole list: %v)", i, got[i].DisplayName, name, display(got))
		}
	}
}

// One person's list, not the deployment's. A shortcut that reshuffles because
// a colleague is busy has stopped being a shortcut.
func TestRecentlyChangedIsPerPerson(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	mine, err := db.CreateCustomer(ctx, "Mine")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := db.CreateCustomer(ctx, "Theirs")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool().Exec(ctx, `
		INSERT INTO audit_log (actor_user_id, action, customer_id, outcome) VALUES
			('ann@example.com', 'extension.change', $1, 'ok'),
			('bob@example.com', 'extension.change', $2, 'ok')`, mine.ID, theirs.ID)
	if err != nil {
		t.Fatal(err)
	}

	got, err := db.RecentlyChanged(ctx, "ann@example.com", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DisplayName != "Mine" {
		t.Errorf("ann sees %v; only her own work belongs here", display(got))
	}
}

/*
Changed, not looked at, and not attempted.

Reads outnumber writes by a wide margin, so counting them would fill this with
every customer whose ticket somebody skimmed. A refused write is not work
either — the customer is exactly where it was.
*/
func TestOnlySuccessfulChangesCount(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	read, err := db.CreateCustomer(ctx, "Only Read")
	if err != nil {
		t.Fatal(err)
	}
	refused, err := db.CreateCustomer(ctx, "Refused")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := db.CreateCustomer(ctx, "Changed")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool().Exec(ctx, `
		INSERT INTO audit_log (actor_user_id, action, customer_id, outcome) VALUES
			('ann@example.com', 'tool.invoke',      $1, 'ok'),
			('ann@example.com', 'extension.change', $2, 'denied'),
			('ann@example.com', 'tool.write',       $3, 'ok')`,
		read.ID, refused.ID, changed.ID)
	if err != nil {
		t.Fatal(err)
	}

	got, err := db.RecentlyChanged(ctx, "ann@example.com", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DisplayName != "Changed" {
		t.Errorf("got %v, want only the one that was actually changed", display(got))
	}
}

// Somebody who has changed nothing gets an empty list rather than an error or
// a null the picker would have to guard against.
func TestRecentlyChangedForSomebodyNew(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	got, err := db.RecentlyChanged(ctx, "nobody@example.com", 5)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("a new person gets null instead of an empty list")
	}
	if len(got) != 0 {
		t.Errorf("a person who has changed nothing has %v", display(got))
	}
}

// display names a list of customers, for a failure message that reads.
func display(customers []store.Customer) []string {
	names := make([]string, 0, len(customers))
	for _, c := range customers {
		names = append(names, c.DisplayName)
	}
	return names
}
