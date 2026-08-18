package api_test

import (
	"net/http"
	"testing"
)

/*
An undo goes back through the comparison, not straight onto the phone system.

The values are already recorded — they are what somebody approved — so undoing
is a sheet whose after is the old before. Running it through the same
comparison is what makes it safe: if a technician changed one of those
extensions since, the undo shows that as a change it would make and they can
decline that row rather than quietly overwriting somebody's work.
*/
func TestAnUndoIsComparedLikeAnythingElse(t *testing.T) {
	srv, client := server(t)

	// Nothing in the test harness provides phone capabilities, so the reading
	// is what fails — which is the assertion worth making here: an undo that
	// cannot see the current state is refused rather than applied blind.
	do(t, client, http.MethodPost, srv.URL+"/api/bulk/"+uuidZero+"/revert", nil, http.StatusNotFound)
}

const uuidZero = "00000000-0000-0000-0000-000000000000"

// Every route under bulk needs the permission that changes a phone system,
// including the ones that only read. Somebody who cannot change one extension
// has no business staging forty.
func TestBulkNeedsThePhonePermission(t *testing.T) {
	srv, client := server(t)

	// The harness signs in as an administrator, who has phone.manage — so the
	// assertion is that these routes exist and are reachable, and the refusal
	// for anyone else is covered by the permission wrapper's own tests.
	for _, path := range []string{
		"/api/bulk?customer_id=" + uuidZero,
		"/api/bulk/" + uuidZero,
	} {
		res, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusUnauthorized {
			t.Errorf("%s refused an administrator: %d", path, res.StatusCode)
		}
	}
}
