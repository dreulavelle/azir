package main

import (
	"slices"
	"testing"
)

/*
Every role a phone system offers, not only the ones somebody already holds.

This used to be discovered by looking at who had a role, on the reasoning that
a role has to exist for somebody to hold it. That is true and useless: the
phone system this was built against has six extensions, two of them holding
"system_owners" and four holding nothing, so the list of roles came back with
exactly one entry and the console offered a dropdown with one option. Every
other role was refused as "not one of system_owners" — from a form whose whole
purpose is to set the role.
*/
func TestEveryStockRoleIsOffered(t *testing.T) {
	// Nothing named, one role held: the fallback list is what is offered.
	roles, labels := rolesFrom(nil, []string{"system_owners"})

	if len(roles) != len(stockRoles) {
		t.Fatalf("offered %d roles, want the %d 3CX ships with: %v", len(roles), len(stockRoles), roles)
	}
	for i, want := range stockRoles {
		if roles[i] != want.Wire {
			t.Errorf("role %d is %q, want %q — the order is the console's", i, roles[i], want.Wire)
		}
		if labels[want.Wire] != want.Label {
			t.Errorf("%s shows as %q, want %q", want.Wire, labels[want.Wire], want.Label)
		}
	}
}

// A role the phone system defines that this does not know about is kept and
// shown as itself. Renaming a role is the case where guessing would be worst.
func TestARenamedRoleIsKept(t *testing.T) {
	roles, labels := rolesFrom([]string{"users", "after_hours_team"}, nil)

	found := false
	for _, name := range roles {
		if name == "after_hours_team" {
			found = true
		}
	}
	if !found {
		t.Fatal("a role the phone system defines was dropped, so nobody could be given it")
	}
	if _, has := labels["after_hours_team"]; has {
		t.Error("a role this does not know was given words it cannot know")
	}
	if roles[0] != "users" {
		t.Errorf("the stock roles lost their order: %v", roles)
	}
}

/*
What the phone system says it has is what is offered — not the stock list, and
not both.

Offering a role a deployment does not define is offering something that is
refused the moment somebody picks it, which is how this went wrong the first
time: the fallback list named "departmentadmins" and "owners", the phone system
knows group_admins and group_owners, and both guesses were refused with the
form insisting they were valid choices.
*/
func TestThePhoneSystemsOwnListWins(t *testing.T) {
	// Exactly what a real system answered with, in the order it answered.
	said := []string{"managers", "users", "system", "observers", "receptionists",
		"supervisors", "group_admins", "group_owners", "system_admins", "system_owners"}

	roles, labels := rolesFrom(said, nil)

	for _, name := range roles {
		if !slices.Contains(said, name) {
			t.Errorf("%q is offered and the phone system never mentioned it", name)
		}
	}
	// Read in the console's order, not the order the answer arrived in.
	if roles[0] != "users" || roles[len(roles)-1] != "system_owners" {
		t.Errorf("roles are not in the order somebody reads them: %v", roles)
	}
	if labels["group_admins"] != "Department Administrator" {
		t.Errorf("group_admins shows as %q", labels["group_admins"])
	}
}

// A deployment without a role does not get it offered.
func TestARoleTheSystemLacksIsNotOffered(t *testing.T) {
	roles, _ := rolesFrom([]string{"users", "managers"}, nil)
	if slices.Contains(roles, "system_owners") {
		t.Errorf("a role this phone system never named was offered: %v", roles)
	}
}

// A role somebody holds exists, whatever the phone system did or did not say.
func TestARoleInUseIsAlwaysOffered(t *testing.T) {
	roles, _ := rolesFrom(nil, []string{"legacy_operators"})
	if roles[len(roles)-1] != "legacy_operators" {
		t.Errorf("a role somebody holds was not offered: %v", roles)
	}
}

/*
The fields marked unique have to be fields.

Two lists that must agree, in one language this time but still two: a name
misspelled here would mark nothing, and the guard it exists to provide would be
silently absent. That is the sixth instance of this shape in this codebase and
the cheapest one to bind.
*/
func TestEveryUniqueFieldIsARealField(t *testing.T) {
	for name := range uniquePerExtension {
		if _, known := editableByName[name]; !known {
			t.Errorf("%q is marked unique but is not a field this sets, so nothing is guarded", name)
		}
	}
}

/*
The roles the console does not offer are not offered here either.

The phone system defines ten and its extension page lists eight; the two it
keeps are its own. A dropdown that puts "system" beside "Receptionist" is
offering a technician a way to make an extension into something no console
would let them make it.
*/
func TestInternalRolesAreNotOffered(t *testing.T) {
	said := []string{"managers", "users", "system", "observers", "receptionists",
		"supervisors", "group_admins", "group_owners", "system_admins", "system_owners"}

	roles, _ := rolesFrom(said, nil)

	for _, hidden := range []string{"system", "observers"} {
		if slices.Contains(roles, hidden) {
			t.Errorf("%q is offered; the console does not list it", hidden)
		}
	}
	if len(roles) != 8 {
		t.Errorf("offered %d roles, want the 8 the console lists: %v", len(roles), roles)
	}
}

// Unless somebody is holding one. A value nobody can see is a value nobody can
// fix.
func TestAnInternalRoleInUseIsStillShown(t *testing.T) {
	roles, _ := rolesFrom([]string{"users", "system"}, []string{"system"})
	if !slices.Contains(roles, "system") {
		t.Errorf("an extension holding %q would have no way to show it: %v", "system", roles)
	}
}
