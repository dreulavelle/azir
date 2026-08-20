package store_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/logging"

	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/internal/testsupport"
	"github.com/dreulavelle/azir/internal/vault"
)

// testDB connects to the package's database and resets the schema, so tests
// are isolated without a database per test. These run against real Postgres
// because mocking a store proves nothing about the SQL, which is the part that
// actually breaks. TestMain resolves where that database comes from.
func testDB(t *testing.T) *store.DB {
	t.Helper()
	return testsupport.DB(t, testDSN)
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
	creds := store.NewCredentials(db, testVault(t), nil)

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

/*
Unsealing a credential must teach the log redactor about it.

This is the wiring the canary tests in internal/logging cannot prove. Those
build a handler and register the sentinel themselves, so they demonstrate that
the handler scrubs what it has been told — which it always did. What was
missing was anybody telling it: nothing outside a test ever called Register,
so in a real deployment the literal list was empty and every case below would
have gone straight to stdout.

The assertion is deliberately end-to-end. It opens a credential through the
ordinary path and then logs the value the careless ways, rather than checking
that Register was called and trusting the rest.
*/
func TestOpeningACredentialTeachesTheRedactor(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	var buf bytes.Buffer
	redactor := logging.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	log := slog.New(redactor)
	creds := store.NewCredentials(db, testVault(t), redactor)

	c, err := db.CreateCustomer(ctx, "Ellis Chiropractic")
	if err != nil {
		t.Fatal(err)
	}

	const secret = "AZIR-CANARY-wiring-4c1a77e2-DO-NOT-EMIT"
	if _, err := creds.Put(ctx, &c.ID, "3cx", "extension_password", []byte(secret)); err != nil {
		t.Fatal(err)
	}

	// Before the secret has ever been unsealed the redactor cannot know it.
	// Stated as a precondition so that a version of this test which passes for
	// the wrong reason — a redactor that scrubs everything — fails here.
	buf.Reset()
	log.Info("nothing has been opened yet: " + secret)
	if !strings.Contains(buf.String(), secret) {
		t.Fatal("the redactor scrubbed a value it was never told about; this test would prove nothing")
	}

	if _, err := creds.Open(ctx, &c.ID, "3cx", "extension_password"); err != nil {
		t.Fatal(err)
	}

	// Every route by which a resolved credential has historically escaped.
	// The key-name cases were covered before; the rest depend on the literal
	// list this test exists to prove is populated.
	cases := []struct {
		name string
		emit func()
	}{
		{"as a message", func() { log.Info("connecting with " + secret) }},
		{"as an innocuously-keyed attr", func() { log.Info("auth", "note", secret) }},
		{"inside an error", func() { log.Error("failed", "error", errors.New("using "+secret)) }},
		{"in a formatted string", func() { log.Info(fmt.Sprintf("token=%s", secret)) }},
		{"via a derived logger", func() { log.With("detail", secret).Info("derived") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			tc.emit()
			if strings.Contains(buf.String(), secret) {
				t.Fatalf("an unsealed credential reached the log via %s:\n%s", tc.name, buf.String())
			}
			if buf.Len() == 0 {
				t.Fatal("nothing was logged; the test proves nothing")
			}
		})
	}
}

// Registering the same secret repeatedly must not grow the literal list.
// Credentials are unsealed per request, so an unbounded list is a slow memory
// leak that also makes every log line more expensive.
func TestRepeatedOpensDoNotGrowTheRedactor(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	counter := &countingRedactor{}
	creds := store.NewCredentials(db, testVault(t), counter)

	c, err := db.CreateCustomer(ctx, "Bay Street Legal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Put(ctx, &c.ID, "3cx", "extension_password", []byte("AZIR-CANARY-dedupe-DO-NOT-EMIT")); err != nil {
		t.Fatal(err)
	}

	for range 50 {
		if _, err := creds.Open(ctx, &c.ID, "3cx", "extension_password"); err != nil {
			t.Fatal(err)
		}
	}
	if counter.calls != 50 {
		t.Fatalf("want a registration per open, got %d", counter.calls)
	}
	if n := counter.distinct(); n != 1 {
		t.Fatalf("want 1 distinct secret registered, got %d", n)
	}
}

type countingRedactor struct {
	calls  int
	values []string
}

func (c *countingRedactor) Register(values ...string) {
	c.calls++
	c.values = append(c.values, values...)
}

func (c *countingRedactor) distinct() int {
	seen := map[string]struct{}{}
	for _, v := range c.values {
		seen[v] = struct{}{}
	}
	return len(seen)
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
	if _, err := store.NewCredentials(db, v1, nil).Put(ctx, nil, "syncro", "api_key", []byte(secret)); err != nil {
		t.Fatal(err)
	}

	// Both keys loaded, sealing with version 2.
	v2, err := vault.New(map[int][]byte{1: k1, 2: k2}, 2)
	if err != nil {
		t.Fatal(err)
	}
	creds2 := store.NewCredentials(db, v2, nil)

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
	creds := store.NewCredentials(db, testVault(t), nil)

	ref, err := creds.Put(ctx, nil, "syncro", "api_key", []byte("some-api-key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Delete(ctx, ref.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Delete(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if _, err := creds.Open(ctx, nil, "syncro", "api_key"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("credential readable after delete: %v", err)
	}
}

// Fuzzy lookup is how a name read off a ticket resolves to a customer without
// anyone knowing an identifier, so near-misses and case differences must match.
func TestSearchCustomers(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	for _, name := range []string{"Acme Dental", "Acme Legal", "Northwind Traders"} {
		if _, err := db.CreateCustomer(ctx, name); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		query string
		want  string
	}{
		{"Acme Dental", "Acme Dental"},
		{"acme dentl", "Acme Dental"},      // typo
		{"northwind", "Northwind Traders"}, // case and partial
	}

	for _, tc := range tests {
		got, err := db.SearchCustomers(ctx, tc.query, 10)
		if err != nil {
			t.Fatalf("search %q: %v", tc.query, err)
		}
		if len(got) == 0 {
			t.Errorf("search %q found nothing", tc.query)
			continue
		}
		if got[0].DisplayName != tc.want {
			t.Errorf("search %q ranked %q first, want %q", tc.query, got[0].DisplayName, tc.want)
		}
	}

	// An empty query lists everything rather than matching nothing.
	all, err := db.SearchCustomers(ctx, "  ", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("empty query returned %d customers, want 3", len(all))
	}
}

/*
Only one account can win first-run setup.

Setup used to count the accounts and then create one, which are two statements
with a gap between them. Both requests could look, both see nothing, and both
insert — leaving a deployment with two administrators, one of whom nobody
chose. The route is open to anyone precisely while that gap exists.

Run concurrently and with real contention, because the bug is invisible to a
sequential test: calling CreateFirstUser twice in a row has always returned
ErrSetupComplete the second time.
*/
func TestOnlyOneFirstUserSurvivesARace(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	const racers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created []store.User
		errs    []error
		ready   = make(chan struct{})
	)

	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready // release them together, so the window is actually contested
			u, err := db.CreateFirstUser(ctx,
				fmt.Sprintf("admin%d@example.com", i), "First Admin", "correct-horse-battery")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			created = append(created, u)
		}()
	}
	close(ready)
	wg.Wait()

	if len(created) != 1 {
		t.Fatalf("first-run setup created %d administrators; exactly one may win", len(created))
	}
	if len(errs) != racers-1 {
		t.Fatalf("want %d refusals, got %d", racers-1, len(errs))
	}
	for _, err := range errs {
		if !errors.Is(err, store.ErrSetupComplete) {
			t.Errorf("a losing racer got %v, want ErrSetupComplete", err)
		}
	}

	// And the survivor is an administrator, not merely a row.
	if created[0].Role != "admin" {
		t.Errorf("the first account has role %q, want admin", created[0].Role)
	}

	// Setup stays closed afterwards.
	if _, err := db.CreateFirstUser(ctx, "later@example.com", "Later", "correct-horse-battery"); !errors.Is(err, store.ErrSetupComplete) {
		t.Errorf("setup reopened after completing: %v", err)
	}
}

/*
A sealed credential belongs to the row it was stored in.

Without binding, ciphertext is a portable blob: write access to this table is
enough to copy one customer's PBX password into another customer's row, and
Azir opens it and connects with it — the wrong customer's system, using
credentials nobody granted for it. There is no integrity check that would
notice, because every field involved is one the attacker just wrote.

This performs that exact move at the SQL level, which is the only way to prove
the property. It cannot be reached through the store's own API, and that is the
point: the threat is somebody who is not using the API.
*/
func TestASealedCredentialCannotBeMovedBetweenCustomers(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	creds := store.NewCredentials(db, testVault(t), nil)

	victim, err := db.CreateCustomer(ctx, "Ellis Dental")
	if err != nil {
		t.Fatal(err)
	}
	attacker, err := db.CreateCustomer(ctx, "Kroth Holdings")
	if err != nil {
		t.Fatal(err)
	}

	const secret = "the-victims-pbx-system-owner-password"
	if _, err := creds.Put(ctx, &victim.ID, "3cx", "extension_password", []byte(secret)); err != nil {
		t.Fatal(err)
	}
	// The attacker's own row, so there is somewhere to write the stolen blob.
	if _, err := creds.Put(ctx, &attacker.ID, "3cx", "extension_password", []byte("the-attackers-own-password")); err != nil {
		t.Fatal(err)
	}

	// Lift the victim's sealed bytes into the attacker's row, wholesale.
	if _, err := db.Pool().Exec(ctx, `
		UPDATE credentials AS dst
		SET dek_wrapped = src.dek_wrapped,
		    dek_nonce   = src.dek_nonce,
		    ciphertext  = src.ciphertext,
		    nonce       = src.nonce,
		    key_version = src.key_version
		FROM credentials AS src
		WHERE dst.customer_id = $1 AND src.customer_id = $2
		  AND dst.plugin = '3cx' AND src.plugin = '3cx'`,
		attacker.ID, victim.ID); err != nil {
		t.Fatal(err)
	}

	got, err := creds.Open(ctx, &attacker.ID, "3cx", "extension_password")
	if err == nil {
		if string(got) == secret {
			t.Fatal("a credential moved between customers opened: the victim's password was served for the attacker's PBX")
		}
		t.Fatalf("a tampered credential opened as %q", got)
	}

	// The victim's own row is untouched and still works.
	stillGood, err := creds.Open(ctx, &victim.ID, "3cx", "extension_password")
	if err != nil {
		t.Fatalf("the victim's own credential stopped opening: %v", err)
	}
	if string(stillGood) != secret {
		t.Fatal("the victim's credential did not round-trip")
	}
}

// The same for the other two parts of the scope: a credential stored for one
// plugin or one kind must not open as another.
func TestASealedCredentialCannotBeMovedBetweenPluginsOrKinds(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	creds := store.NewCredentials(db, testVault(t), nil)

	if _, err := creds.Put(ctx, nil, "syncro", "api_key", []byte("a-psa-api-key-worth-having")); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, plugin, kind string }{
		{"another plugin", "3cx", "api_key"},
		{"another kind", "syncro", "webhook_secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Pool().Exec(ctx, `
				INSERT INTO credentials
					(id, customer_id, plugin, kind, dek_wrapped, dek_nonce, ciphertext, nonce, key_version)
				SELECT gen_random_uuid(), NULL, $1, $2, dek_wrapped, dek_nonce, ciphertext, nonce, key_version
				FROM credentials WHERE plugin = 'syncro' AND kind = 'api_key'`,
				tc.plugin, tc.kind); err != nil {
				t.Fatal(err)
			}
			if _, err := creds.Open(ctx, nil, tc.plugin, tc.kind); err == nil {
				t.Fatalf("a credential copied to %s/%s opened", tc.plugin, tc.kind)
			}
		})
	}
}

/*
Disconnecting a plugin from a customer must take the credential with it.

The point of the whole operation is that nothing is left holding the keys. An
orphaned credential is a System Owner password for a phone system nobody
believes is connected any more, sitting sealed in the vault with nothing on any
screen to say it is there — which is worse than never offering the button,
because somebody has been told it is gone.
*/
func TestDisconnectRemovesEverythingThatPluginHeld(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	creds := store.NewCredentials(db, testVault(t), nil)

	them, err := db.CreateCustomer(ctx, "Ellis Dental")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateCustomer(ctx, "Kroth Holdings")
	if err != nil {
		t.Fatal(err)
	}

	if err := db.LinkIdentity(ctx, them.ID, "3cx", "pbx-ellis"); err != nil {
		t.Fatal(err)
	}
	if err := db.LinkIdentity(ctx, them.ID, "syncro", "12345"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPluginConfig(ctx, "3cx", &them.ID,
		map[string]any{"host": "pbx.ellis.example"}, "tech@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Put(ctx, &them.ID, "3cx", "password", []byte("system-owner-password")); err != nil {
		t.Fatal(err)
	}
	// A different customer's 3CX, which must survive untouched.
	if _, err := creds.Put(ctx, &other.ID, "3cx", "password", []byte("someone-elses-password")); err != nil {
		t.Fatal(err)
	}

	gone, err := db.Disconnect(ctx, them.ID, "3cx")
	if err != nil {
		t.Fatal(err)
	}
	if gone.Identities != 1 || gone.Settings != 1 || gone.Credentials != 1 {
		t.Fatalf("removed %+v, want one of each", gone)
	}

	if _, err := creds.Open(ctx, &them.ID, "3cx", "password"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the credential outlived the disconnect: %v", err)
	}
	cfg, err := db.GetPluginConfig(ctx, "3cx", &them.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Values) != 0 {
		t.Errorf("settings outlived the disconnect: %v", cfg.Values)
	}
	if _, err := db.ResolveIdentity(ctx, "3cx", "pbx-ellis"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the identity outlived the disconnect: %v", err)
	}

	// Everything the disconnect was not about is still there. Taking a
	// customer's phone system away must not take their helpdesk with it, and
	// must not touch anybody else's.
	if _, err := db.ResolveIdentity(ctx, "syncro", "12345"); err != nil {
		t.Errorf("disconnecting 3cx removed the syncro link: %v", err)
	}
	if _, err := creds.Open(ctx, &other.ID, "3cx", "password"); err != nil {
		t.Errorf("disconnecting one customer's 3cx removed another's: %v", err)
	}
}

// Disconnecting something that was never connected is not an error, and says
// so by reporting that it removed nothing.
func TestDisconnectingWhatWasNeverThereRemovesNothing(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	them, err := db.CreateCustomer(ctx, "Bay Street Legal")
	if err != nil {
		t.Fatal(err)
	}
	gone, err := db.Disconnect(ctx, them.ID, "3cx")
	if err != nil {
		t.Fatalf("disconnecting an unconnected plugin failed: %v", err)
	}
	if gone.Identities != 0 || gone.Settings != 0 || gone.Credentials != 0 {
		t.Fatalf("removed %+v from a customer with nothing connected", gone)
	}
}
