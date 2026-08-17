package store_test

import (
	"context"
	"testing"
)

// A password change has to end the sessions that password opened.
//
// Otherwise "change the password" does not mean what an administrator thinks it
// means: somebody who has left, or an account that was misused, keeps working
// from whatever browser is already signed in until the session happens to
// expire — which is the one moment the change was made to prevent.
func TestChangingAPasswordSignsThemOut(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	user, err := db.CreateUser(ctx, "tech@example.com", "Tech", "technician", "first-password")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := db.CreateSession(ctx, user.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ActorForSession(ctx, token); err != nil {
		t.Fatalf("the session should work before anything changes: %v", err)
	}

	if err := db.SetUserPassword(ctx, user.ID, "a-much-longer-password"); err != nil {
		t.Fatal(err)
	}
	ended, err := db.EndSessionsFor(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ended != 1 {
		t.Errorf("ended %d sessions, want 1", ended)
	}
	if _, err := db.ActorForSession(ctx, token); err == nil {
		t.Error("the old session still works after the password changed")
	}

	// And the new password is the one that works.
	if _, err := db.Authenticate(ctx, "tech@example.com", "a-much-longer-password"); err != nil {
		t.Errorf("the new password was refused: %v", err)
	}
	if _, err := db.Authenticate(ctx, "tech@example.com", "first-password"); err == nil {
		t.Error("the old password still works")
	}
}

// Disabling is immediate, not "when the session expires".
func TestDisablingEndsAccessAtOnce(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	user, err := db.CreateUser(ctx, "gone@example.com", "Gone", "technician", "a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := db.CreateSession(ctx, user.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetUserDisabled(ctx, user.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ActorForSession(ctx, token); err == nil {
		t.Error("a disabled account still has a working session")
	}
}

// The count that stops a deployment locking itself out has to ignore disabled
// administrators, or disabling one and then demoting the other leaves nobody.
func TestCountAdminsIgnoresDisabledOnes(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	first, err := db.CreateUser(ctx, "one@example.com", "One", "admin", "a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateUser(ctx, "two@example.com", "Two", "admin", "a-long-enough-password"); err != nil {
		t.Fatal(err)
	}

	before, err := db.CountAdmins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetUserDisabled(ctx, first.ID, true); err != nil {
		t.Fatal(err)
	}
	after, err := db.CountAdmins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != before-1 {
		t.Errorf("admins went %d → %d after disabling one; a disabled admin is still counted", before, after)
	}
}

// Removing an account takes its sessions with it.
func TestDeletingAnAccountEndsItsSessions(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	user, err := db.CreateUser(ctx, "temp@example.com", "Temp", "technician", "a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := db.CreateSession(ctx, user.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ActorForSession(ctx, token); err == nil {
		t.Error("a removed account still has a working session")
	}
}
