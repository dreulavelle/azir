package api_test

import (
	"context"
	"net/http"
	"testing"
)

// Every DID route is under the permission that changes a phone system,
// including the ones that only read, for the reason the bulk routes are:
// somebody who cannot change one extension has no business staging two hundred
// numbers onto a trunk.
func TestDIDRoutesNeedThePhonePermission(t *testing.T) {
	srv, client, db := serverWithDB(t)

	customer, err := db.CreateCustomer(context.Background(), "Rainwater Plumbing")
	if err != nil {
		t.Fatal(err)
	}

	// The harness signs in as an administrator, who has phone.manage, so what
	// is asserted here is that these routes exist and are reachable. The
	// refusal for anybody else is the permission wrapper's own test.
	for _, path := range []string{
		"/api/dids/trunks?customer_id=" + customer.ID.String(),
		"/api/dids/starting-sheet?customer_id=" + customer.ID.String(),
	} {
		res, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		switch res.StatusCode {
		case http.StatusForbidden, http.StatusUnauthorized:
			t.Errorf("%s refused an administrator: %d", path, res.StatusCode)
		case http.StatusNotFound:
			t.Errorf("%s is not a route", path)
		}
	}
}

/*
An import applies nothing without the word.

The confirmation is the last thing between a mistyped file and two hundred
numbers on a customer's trunk, so a body without it must be refused whatever
else it says.
*/
func TestAnImportWithoutTheWordChangesNothing(t *testing.T) {
	srv, client, db := serverWithDB(t)

	customer, err := db.CreateCustomer(context.Background(), "Rainwater Plumbing")
	if err != nil {
		t.Fatal(err)
	}

	do(t, client, http.MethodPost,
		srv.URL+"/api/dids/import?customer_id="+customer.ID.String(),
		map[string]any{
			"trunk": "10000",
			"dids":  []map[string]string{{"number": "+15551110000"}},
		},
		http.StatusBadRequest)
}

// An import naming no numbers is refused rather than sent to a phone system as
// an empty write.
func TestAnImportOfNothingIsRefused(t *testing.T) {
	srv, client, db := serverWithDB(t)

	customer, err := db.CreateCustomer(context.Background(), "Rainwater Plumbing")
	if err != nil {
		t.Fatal(err)
	}

	do(t, client, http.MethodPost,
		srv.URL+"/api/dids/import?customer_id="+customer.ID.String(),
		map[string]any{"trunk": "10000", "confirm": "import", "dids": []any{}},
		http.StatusBadRequest)
}

// An import naming no trunk is refused. Guessing which trunk a carrier's
// numbers belong on is not a guess worth making.
func TestAnImportWithNoTrunkIsRefused(t *testing.T) {
	srv, client, db := serverWithDB(t)

	customer, err := db.CreateCustomer(context.Background(), "Rainwater Plumbing")
	if err != nil {
		t.Fatal(err)
	}

	do(t, client, http.MethodPost,
		srv.URL+"/api/dids/import?customer_id="+customer.ID.String(),
		map[string]any{
			"confirm": "import",
			"dids":    []map[string]string{{"number": "+15551110000"}},
		},
		http.StatusBadRequest)
}
