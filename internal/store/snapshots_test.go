package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

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

// setPBX points a customer's 3cx settings at an address.
func setPBX(t *testing.T, db *store.DB, customer store.Customer, fqdn string) {
	t.Helper()
	if err := db.SetPluginConfig(context.Background(), "3cx", &customer.ID,
		map[string]any{"fqdn": fqdn}, "test"); err != nil {
		t.Fatal(err)
	}
}

// A bundle carries the phone system's own address, and that address is what an
// administrator already typed into that customer's settings. Matching them is
// what saves anybody choosing from a list.
func TestBundleFindsItsCustomerByAddress(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	theirs, err := db.CreateCustomer(ctx, "Stonebridge")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateCustomer(ctx, "Rainwater")
	if err != nil {
		t.Fatal(err)
	}
	setPBX(t, db, theirs, "stonebridge.la.3cx.us")
	setPBX(t, db, other, "rainfirm.3cx.us")

	// Case should not matter: an FQDN is not case-sensitive and the two are
	// typed by different people at different times.
	found, err := db.CustomerByPluginValue(ctx, "3cx", "fqdn", "StoneBridge.LA.3CX.us")
	if err != nil {
		t.Fatal(err)
	}
	if found != theirs.ID {
		t.Errorf("matched %s, want %s", found, theirs.ID)
	}

	unknown, err := db.CustomerByPluginValue(ctx, "3cx", "fqdn", "nobody.3cx.us")
	if err != nil {
		t.Fatal(err)
	}
	if unknown != uuid.Nil {
		t.Errorf("matched %s to an address nobody has", unknown)
	}
}

/*
Two customers sharing an address must match neither.

This is not hypothetical: a trial PBX ends up pointed at several customer
records while somebody is setting Azir up. Picking one would file a customer's
extension numbers and administrators' names under a different customer, with
nothing on the page to say it was a guess.
*/
func TestSharedAddressAttachesToNobody(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	first, err := db.CreateCustomer(ctx, "One")
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateCustomer(ctx, "Two")
	if err != nil {
		t.Fatal(err)
	}
	setPBX(t, db, first, "shared.3cx.us")
	setPBX(t, db, second, "shared.3cx.us")

	found, err := db.CustomerByPluginValue(ctx, "3cx", "fqdn", "shared.3cx.us")
	if err != nil {
		t.Fatal(err)
	}
	if found != uuid.Nil {
		t.Errorf("attached to %s when two customers claim that address", found)
	}
}

// A capture arrives before anybody has decided whose it is.
func TestSnapshotSurvivesWithoutACustomer(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	saved, err := db.AddSnapshot(ctx, store.Snapshot{
		FQDN:   "nobody.3cx.us",
		Report: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("an unattached capture was refused: %v", err)
	}
	if saved.ExpiresAt == nil {
		t.Error("no expiry was set; diagnostic data has to go away on its own")
	}

	owner, err := db.CreateCustomer(ctx, "Found them")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AttachSnapshot(ctx, saved.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetSnapshot(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CustomerID != owner.ID {
		t.Errorf("attached to %s, want %s", got.CustomerID, owner.ID)
	}
}

// Keeping one clears its expiry; the sweep takes the rest.
func TestExpiredCapturesAreSweptAndKeptOnesAreNot(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	past := time.Now().Add(-time.Hour)
	doomed, err := db.AddSnapshot(ctx, store.Snapshot{
		FQDN: "old.3cx.us", Report: json.RawMessage(`{}`), ExpiresAt: &past,
	})
	if err != nil {
		t.Fatal(err)
	}
	spared, err := db.AddSnapshot(ctx, store.Snapshot{
		FQDN: "keep.3cx.us", Report: json.RawMessage(`{}`), ExpiresAt: &past,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.KeepSnapshot(ctx, spared.ID, true); err != nil {
		t.Fatal(err)
	}

	if _, err := db.SweepSnapshots(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetSnapshot(ctx, doomed.ID); !errors.Is(err, store.ErrNotFound) {
		t.Error("an expired capture outlived its expiry")
	}
	if _, err := db.GetSnapshot(ctx, spared.ID); err != nil {
		t.Errorf("a kept capture was swept anyway: %v", err)
	}
}
