package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
)

/*
What an extension may be given as a role.

3CX stores a role as a name on the group membership beside the extension, and
documents RoleName as a plain string with no list of what it accepts. So the
list has to come from somewhere, and there are three places to get it, in
descending order of how much they can be trusted:

 1. The phone system itself. /MyGroup/Rights is the set of roles defined on the
    group, which is the real answer and picks up any that were renamed.
 2. The roles 3CX ships with, below. Every deployment has these.
 3. The roles somebody on this system is holding.

This used to be only the third, and the third is the one that cannot work.
A role has to be held by somebody to be discovered, so a phone system where
everybody is an ordinary user offers exactly one role — its own — and every
other one is refused as "not one of". On the system this was built against that
meant a single choice, "system_owners", and no way to make anybody a
receptionist.
*/

/*
internalRoles are roles the phone system defines and its console does not offer.

Reading the roles from the phone system turned up ten where the extension page
lists eight. The two extra are the phone system's own — a service account is a
"system", not a person — and putting them in a dropdown beside Receptionist is
offering somebody a way to make an extension into something no console would
let them make it.

Not hidden, exactly: an extension already holding one still shows it, because a
value somebody cannot see is a value they cannot fix.
*/
var internalRoles = map[string]bool{
	"system":    true,
	"observers": true,
}

// role is one of the roles 3CX ships with: the name it stores, and the words
// the console shows for it.
type role struct {
	Wire  string
	Label string
}

/*
stockRoles is what 3CX ships with, in the order its own dropdown lists them.

Least authority first, which is the order somebody reads them in and the order
the console presents. Kept as the order to publish rather than sorted, because
a permission list sorted alphabetically puts "System Owner" between "Supervisor"
and "User" and reads as though it means nothing.

The names were guessed once, from the shape of the only one that had been read
off a real system, and two of the eight were wrong: "departmentadmins" and
"owners" are group_admins and group_owners, and the phone system refused both
guesses outright. They are right now because the phone system was asked — see
rolesDefined — and this table is what remains for the case where it will not
answer. That is the whole lesson: a list of what something accepts belongs to
that something, and inferring one from a naming pattern is guessing with extra
steps.
*/
var stockRoles = []role{
	{"users", "User"},
	{"receptionists", "Receptionist"},
	{"supervisors", "Supervisor"},
	{"group_admins", "Department Administrator"},
	{"managers", "Manager"},
	{"group_owners", "Owner"},
	{"system_admins", "System Administrator"},
	{"system_owners", "System Owner"},
}

/*
rolesAvailable is every role this extension could be given, and what to call
each one.

Asked of the phone system first and answered from the stock list when it will
not say — either answer is better than the old one, which was to look at who
already held a role and offer only that.

Anything the phone system reports that is not a stock role is kept and shown as
itself. A renamed or custom role is exactly the case where guessing would be
worst, and a name nobody can read is still better than a role somebody cannot
assign.
*/
func rolesAvailable(ctx context.Context, conn pbx, groups map[string]int64, inUse []string) ([]string, map[string]string) {
	return rolesFrom(rolesDefined(ctx, conn, groups), inUse)
}

// rolesFrom is rolesAvailable once the phone system has been asked, kept apart
// so that what it decides can be tested without one.
func rolesFrom(defined, inUse []string) ([]string, map[string]string) {
	known := map[string]bool{}
	var order []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || known[name] {
			return
		}
		known[name] = true
		order = append(order, name)
	}

	if len(defined) > 0 {
		// The phone system answered, so these are the roles — not the ones
		// 3CX usually ships with, and not a union of the two. Offering a role
		// this deployment does not define is offering something that will be
		// refused at the moment somebody tries it.
		//
		// Ordered the way the console lists them where the name is one this
		// package recognises, because the answer comes back unordered and a
		// permission list in arbitrary order reads as though it means nothing.
		have := map[string]bool{}
		for _, name := range defined {
			have[strings.TrimSpace(name)] = true
		}
		for _, r := range stockRoles {
			if have[r.Wire] {
				add(r.Wire)
			}
		}
		for _, name := range defined {
			if !internalRoles[name] {
				add(name)
			}
		}
	} else {
		for _, r := range stockRoles {
			add(r.Wire)
		}
	}

	// Only ever additive: a role somebody holds exists, whatever else did or
	// did not answer.
	for _, name := range inUse {
		add(name)
	}

	labels := map[string]string{}
	for _, r := range stockRoles {
		if known[r.Wire] {
			labels[r.Wire] = r.Label
		}
	}
	return order, labels
}

/*
rolesDefined asks the phone system which roles its group defines.

Answers nothing rather than an error. A phone system that will not describe its
own roles is not a reason to refuse to draw the form — the stock list still
covers it, and the only thing lost is a role somebody renamed.
*/
func rolesDefined(ctx context.Context, conn pbx, groups map[string]int64) []string {
	// A group's own rights first. MyGroup/Rights answers 403 unless the
	// account itself holds the system owner role, and asking about a group by
	// id does not — so the deployment that most needs this list is the one
	// that could not get it.
	for _, id := range groups {
		if names := rightsAt(ctx, conn, fmt.Sprintf("Groups(%d)/Rights", id)); len(names) > 0 {
			slog.Debug("the phone system named its roles", "roles", fmt.Sprintf("%q", names))
			return names
		}
	}
	return rightsAt(ctx, conn, "MyGroup/Rights")
}

func rightsAt(ctx context.Context, conn pbx, path string) []string {
	var answer struct {
		Value []struct {
			RoleName string `json:"RoleName"`
		} `json:"value"`
	}
	query := url.Values{"$select": {"RoleName"}}
	if err := conn.get(ctx, path, query, &answer); err != nil {
		slog.Info("the phone system would not name its roles here",
			"where", path, "why", err)
		return nil
	}
	names := make([]string, 0, len(answer.Value))
	for _, r := range answer.Value {
		names = append(names, r.RoleName)
	}
	slog.Debug("the phone system named its roles", "roles", names)
	return names
}
