package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dreulavelle/azir/internal/store"
)

func capture(t *testing.T, db *store.DB, customer store.Customer, filename string) store.Snapshot {
	t.Helper()
	saved, err := db.AddSnapshot(context.Background(), store.Snapshot{
		CustomerID: customer.ID,
		Kind:       "3cx-support-info",
		Filename:   filename,
		Report:     json.RawMessage(`{"system":{"version":"20.0"}}`),
		Findings:   1,
		Worst:      "critical",
		UploadedBy: "tech@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// A support capture is one customer's telephony — their extensions, their
// trunk addresses, the numbers people rang. Listing must never reach across.
func TestSnapshotsAreScopedToTheirCustomer(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	one, err := db.CreateCustomer(ctx, "Rainwater")
	if err != nil {
		t.Fatal(err)
	}
	two, err := db.CreateCustomer(ctx, "Stonebridge")
	if err != nil {
		t.Fatal(err)
	}

	mine := capture(t, db, one, "rainwater.zip")
	capture(t, db, two, "stonebridge.zip")

	found, err := db.Snapshots(ctx, one.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("got %d captures for one customer, want only their own", len(found))
	}
	if found[0].ID != mine.ID {
		t.Errorf("listed %s, want %s", found[0].ID, mine.ID)
	}
}

// The assistant is allowed to read a capture only for the customer the
// conversation is about, and it decides that by comparing the capture's owner
// against that customer. A GetSnapshot that left CustomerID unset would make
// every one of those comparisons fail closed — the tool would refuse captures
// it should serve, and the reason would be invisible.
func TestGetSnapshotKnowsWhoItBelongsTo(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	owner, err := db.CreateCustomer(ctx, "Rainwater")
	if err != nil {
		t.Fatal(err)
	}
	saved := capture(t, db, owner, "rainwater.zip")

	got, err := db.GetSnapshot(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CustomerID != owner.ID {
		t.Errorf("capture belongs to %s, want %s", got.CustomerID, owner.ID)
	}
	if len(got.Report) == 0 {
		t.Error("no report came back; the reader has nothing to answer from")
	}
}
