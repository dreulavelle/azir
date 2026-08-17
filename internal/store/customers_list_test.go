package store_test

import (
	"context"
	"testing"
)

// The list has to carry identities. A caller holding the id a connected system
// knows a customer by has no other way to find them, and an empty slice was
// indistinguishable from "this customer is not linked to anything".
func TestListCustomersCarriesIdentities(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	linked, err := db.CreateCustomer(ctx, "Ironvale Construction")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.LinkIdentity(ctx, linked.ID, "psa", "36244200"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateCustomer(ctx, "Nobody In Particular"); err != nil {
		t.Fatal(err)
	}

	all, err := db.ListCustomers(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, c := range all {
		if c.ID != linked.ID {
			continue
		}
		found = true
		if len(c.Identities) != 1 {
			t.Fatalf("expected one identity, got %d", len(c.Identities))
		}
		if c.Identities[0].ExternalID != "36244200" || c.Identities[0].Plugin != "psa" {
			t.Errorf("wrong identity: %+v", c.Identities[0])
		}
	}
	if !found {
		t.Fatal("the linked customer is missing from the list")
	}

	// A customer with no identities must come back with an empty slice rather
	// than null, so a caller can range over it without checking.
	for _, c := range all {
		if c.Identities == nil {
			t.Errorf("%s has nil identities", c.DisplayName)
		}
	}
}
