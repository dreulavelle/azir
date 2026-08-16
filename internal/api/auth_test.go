package api_test

import (
	"testing"

	"github.com/dreulavelle/azir/internal/identity"
)

// Permissions are checked by name so that adding a role later is a row in a
// table rather than an edit to every call site. This guards the property that
// makes that true: nothing may check a role name.
func TestPermissionsAreCheckedByName(t *testing.T) {
	technician := identity.Actor{
		Role:        "technician",
		Permissions: []string{identity.PermToolRead, identity.PermTicketComment},
	}

	if !technician.Can(identity.PermTicketComment) {
		t.Error("technician cannot comment")
	}
	if technician.Can(identity.PermPluginConfigure) {
		t.Error("technician can configure plugins; that is how someone widens their own access")
	}
	if technician.Can(identity.PermUserManage) {
		t.Error("technician can manage users")
	}

	// A role name grants nothing on its own.
	impostor := identity.Actor{Role: "admin", Permissions: nil}
	if impostor.Can(identity.PermPluginConfigure) {
		t.Error("a role named admin was treated as permission by itself")
	}
}

func TestPasswordHashing(t *testing.T) {
	const password = "a-sufficiently-long-password"

	hash, err := identity.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if hash == password {
		t.Fatal("the password was stored in the clear")
	}
	if !identity.VerifyPassword(hash, password) {
		t.Error("the correct password was rejected")
	}
	if identity.VerifyPassword(hash, password+"x") {
		t.Error("a wrong password was accepted")
	}

	// Two hashes of one password must differ, or equal passwords are visible
	// to anyone who reads the table.
	other, err := identity.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if hash == other {
		t.Error("hashing is deterministic; equal passwords would be identifiable")
	}

	if _, err := identity.HashPassword("short"); err == nil {
		t.Error("a trivially short password was accepted")
	}
}

// A session token must never be stored as given, so a copy of the database is
// not a set of working logins.
func TestSessionTokensAreStoredHashed(t *testing.T) {
	token, hash, err := identity.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if token == hash {
		t.Fatal("the session token is stored verbatim")
	}
	if identity.HashToken(token) != hash {
		t.Error("hashing is not reproducible, so no session could ever be resolved")
	}
	if len(token) < 32 {
		t.Errorf("session token is only %d characters", len(token))
	}
}
