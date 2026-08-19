package main

import (
	"context"
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

The words are the console's. The names beside them are 3CX's own, and only
"system_owners" has been read back off a real system — the rest follow its
shape. That is why rolesDefined is asked first: where the phone system will
name its own roles, its names win, and these are only what is offered when it
will not. Giving the API account the system owner role is what makes it answer,
and is what 3CX's own instructions for creating one say to do.

What a wrong name would do is not known: whether 3CX refuses one or stores it
has not been established, because the only system available to try it on has
two extensions in a group and both belong to real people. So this is the part
to be suspicious of, and the answer is not to guess harder — it is to let the
phone system name its own roles, which it will as soon as the account is
allowed to ask.
*/
var stockRoles = []role{
	{"users", "User"},
	{"receptionists", "Receptionist"},
	{"supervisors", "Supervisor"},
	{"departmentadmins", "Department Administrator"},
	{"managers", "Manager"},
	{"owners", "Owner"},
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
func rolesAvailable(ctx context.Context, conn pbx, inUse []string) ([]string, map[string]string) {
	return rolesFrom(rolesDefined(ctx, conn), inUse)
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

	// The stock roles first, so the common ones keep the console's order
	// whatever else turns up.
	for _, r := range stockRoles {
		add(r.Wire)
	}
	for _, name := range defined {
		add(name)
	}
	// Last, and only ever additive: a role somebody holds exists, whatever
	// else did or did not answer.
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
func rolesDefined(ctx context.Context, conn pbx) []string {
	var answer struct {
		Value []struct {
			RoleName string `json:"RoleName"`
		} `json:"value"`
	}
	query := url.Values{"$select": {"RoleName"}}
	if err := conn.get(ctx, "MyGroup/Rights", query, &answer); err != nil {
		// Worth saying out loud rather than swallowing: 3CX answers this
		// with 403 unless the API account itself holds the system owner
		// role, and an account without it leaves this offering the roles
		// 3CX ships with rather than the ones this system actually has.
		slog.Info("the phone system would not name its roles, so the standard ones are offered",
			"why", err)
		return nil
	}
	names := make([]string, 0, len(answer.Value))
	for _, r := range answer.Value {
		names = append(names, r.RoleName)
	}
	slog.Debug("the phone system named its roles", "roles", names)
	return names
}
