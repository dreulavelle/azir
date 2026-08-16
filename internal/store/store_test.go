package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/internal/vault"
)

// testDB connects to the package's database and resets the schema, so tests
// are isolated without a database per test. These run against real Postgres
// because mocking a store proves nothing about the SQL, which is the part that
// actually breaks. TestMain resolves where that database comes from.
func testDB(t *testing.T) *store.DB {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, testDSN)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(db.Close)

	// Extensions live in the shared schema; objects do not.
	if _, err := db.Pool().Exec(ctx,
		`DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func testVault(t *testing.T) *vault.Vault {
	t.Helper()
	key := make([]byte, vault.KeySize)
	for i := range key {
		key[i] = byte(i * 7)
	}
	v, err := vault.New(map[int][]byte{1: key}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCustomerSpine(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	// A customer with no identities is valid — a walk-in with no PSA record
	// still gets memory.
	c, err := db.CreateCustomer(ctx, "Acme Dental")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.CreateCustomer(ctx, "acme dental"); err == nil {
		t.Error("duplicate display name accepted; two rows would accumulate separate memory")
	}

	if err := db.LinkIdentity(ctx, c.ID, "syncro", "12345"); err != nil {
		t.Fatal(err)
	}
	if err := db.LinkIdentity(ctx, c.ID, "3cx", "pbx-instance-7"); err != nil {
		t.Fatal(err)
	}

	// Resolution is the whole point: a Syncro ticket becomes an Azir customer
	// without Syncro defining one.
	resolved, err := db.ResolveIdentity(ctx, "syncro", "12345")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != c.ID {
		t.Fatalf("resolved wrong customer: %s vs %s", resolved.ID, c.ID)
	}
	if len(resolved.Identities) != 2 {
		t.Errorf("want 2 identities, got %d", len(resolved.Identities))
	}

	if _, err := db.ResolveIdentity(ctx, "syncro", "does-not-exist"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestCredentialsSealedAtRest(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	creds := store.NewCredentials(db, testVault(t))

	c, err := db.CreateCustomer(ctx, "Acme Dental")
	if err != nil {
		t.Fatal(err)
	}

	const secret = "AZIR-CANARY-store-b7f3e91d-DO-NOT-EMIT"
	if _, err := creds.Put(ctx, &c.ID, "3cx", "extension_password", []byte(secret)); err != nil {
		t.Fatal(err)
	}

	// The canary must not be readable from the table by any means.
	var count int
	if err := db.Pool().QueryRow(ctx, `
		SELECT count(*) FROM credentials
		WHERE position($1::bytea in ciphertext) > 0
		   OR position($1::bytea in dek_wrapped) > 0`, []byte(secret)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("secret is present in the credentials table as plaintext")
	}

	got, err := creds.Open(ctx, &c.ID, "3cx", "extension_password")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != secret {
		t.Fatal("credential did not round-trip intact")
	}

	// Listing exposes references only; there is no path that enumerates values.
	refs, err := creds.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("want 1 credential ref, got %d", len(refs))
	}
	if strings.Contains(refs[0].Kind, secret) {
		t.Fatal("secret leaked into a credential reference")
	}
}

// Rotation must re-wrap onto the new key while leaving secrets readable.
func TestCredentialRotation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	k1 := make([]byte, vault.KeySize)
	k2 := make([]byte, vault.KeySize)
	for i := range k1 {
		k1[i], k2[i] = byte(i), byte(255-i)
	}

	v1, err := vault.New(map[int][]byte{1: k1}, 1)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "rotate-me-please"
	if _, err := store.NewCredentials(db, v1).Put(ctx, nil, "syncro", "api_key", []byte(secret)); err != nil {
		t.Fatal(err)
	}

	// Both keys loaded, sealing with version 2.
	v2, err := vault.New(map[int][]byte{1: k1, 2: k2}, 2)
	if err != nil {
		t.Fatal(err)
	}
	creds2 := store.NewCredentials(db, v2)

	moved, err := creds2.Rotate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Fatalf("want 1 credential rewrapped, got %d", moved)
	}

	got, err := creds2.Open(ctx, nil, "syncro", "api_key")
	if err != nil {
		t.Fatalf("credential unreadable after rotation: %v", err)
	}
	if string(got) != secret {
		t.Fatal("rotation corrupted the secret")
	}

	// Rotation is idempotent: nothing left on an old key.
	again, err := creds2.Rotate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("want 0 on second rotation, got %d", again)
	}
}

// Discovery proposes; an administrator disposes. Rediscovery must never
// launder a rejection back into pending.
func TestCapabilityGate(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := db.Observe(ctx, "syncro", "tickets.search", []string{"work_items.search"}); err != nil {
		t.Fatal(err)
	}

	approved, err := db.ApprovedTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(approved) != 0 {
		t.Fatal("a newly discovered tool was usable without approval")
	}

	if err := db.Decide(ctx, "syncro", "tickets.search", store.StatusApproved, "admin"); err != nil {
		t.Fatal(err)
	}
	approved, err = db.ApprovedTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := approved["syncro.tickets.search"]; !ok {
		t.Fatal("approved tool is not usable")
	}

	// A rejected tool must stay rejected across restarts.
	if err := db.Decide(ctx, "syncro", "tickets.search", store.StatusRejected, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := db.Observe(ctx, "syncro", "tickets.search", []string{"work_items.search"}); err != nil {
		t.Fatal(err)
	}
	records, err := db.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Status != store.StatusRejected {
		t.Fatalf("rediscovery reset an administrator decision: %+v", records)
	}

	if err := db.Decide(ctx, "ghost", "nope", store.StatusApproved, "admin"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("want ErrNotFound deciding an unknown tool, got %v", err)
	}
}

func TestDeleteCredential(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	creds := store.NewCredentials(db, testVault(t))

	ref, err := creds.Put(ctx, nil, "syncro", "api_key", []byte("some-api-key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.Delete(ctx, ref.ID); err != nil {
		t.Fatal(err)
	}
	if err := creds.Delete(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if _, err := creds.Open(ctx, nil, "syncro", "api_key"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("credential readable after delete: %v", err)
	}
}
