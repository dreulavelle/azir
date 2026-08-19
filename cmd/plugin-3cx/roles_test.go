package main

import "testing"

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
	// What the real system reported: one role, held by the two real people.
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
