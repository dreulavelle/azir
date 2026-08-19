package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
)

/*
The sets of values a field will accept, asked of the phone system.

A field whose valid values are a list is a field that should be a dropdown, and
the list has to come from the phone system rather than from a constant here —
every deployment has its own departments, and its own way in for handsets.

Anything that cannot be asked for is answered as an empty list, and a field
with no choices is published as one somebody types into rather than picks from.
That is a worse form than the alternative and a much better one than a form
that refuses every value it was not told about in advance, which is what the
role dropdown did for as long as its list came from looking at who held one.
*/

// choices is a set of values a field accepts, with what to show for each.
type choices struct {
	Values []string
	Labels map[string]string
}

/*
departmentsAvailable is the groups this phone system has.

3CX calls them groups and the console calls them departments; they are the same
thing, and an extension's department is the group its membership names. Read by
number and name, because two groups can share a display name and the number is
what distinguishes them.
*/
func departmentsAvailable(ctx context.Context, conn pbx) (choices, map[string]int64) {
	groups, err := readGroups(ctx, conn)
	if err != nil {
		slog.Info("the phone system would not list its departments", "why", err)
		return choices{}, nil
	}

	out := choices{Labels: map[string]string{}}
	ids := make(map[string]int64, len(groups))
	for _, g := range groups {
		name := strings.TrimSpace(g.Name)
		if name == "" {
			continue
		}
		out.Values = append(out.Values, name)
		ids[name] = g.ID
		// The number is worth showing beside the name: DEFAULT and Sales read
		// as different things, but two groups both called Support do not.
		if number := strings.TrimSpace(g.Number); number != "" {
			out.Labels[name] = fmt.Sprintf("%s (%s)", name, number)
		}
	}
	sort.Strings(out.Values)
	return out, ids
}

// group is a department as the phone system keeps it.
type group struct {
	ID     int64  `json:"Id"`
	Name   string `json:"Name"`
	Number string `json:"Number"`
}

/*
readGroups fetches the departments, asking as plainly as possible.

No $top and no $select. This phone system answers 400 to a $select naming two
properties on this collection, in the same way it answers 400 to a large $top
on Users — the OData surface is narrower than the specification suggests, and
the way to find that out is one refusal at a time. Asking for everything and
reading the three fields that matter costs a slightly larger response and
works.
*/
func readGroups(ctx context.Context, conn pbx) ([]group, error) {
	var answer struct {
		Value []group `json:"value"`
	}
	if err := conn.get(ctx, "Groups", nil, &answer); err != nil {
		return nil, err
	}
	return answer.Value, nil
}

/*
routingDevices is where a desk phone fetches its configuration from.

The phone system itself, and any session border controller in front of it. The
value on a provisioned handset is the name of one of them, so the list is the
FQDN this plugin is configured against plus whatever /Sbcs reports.

The FQDN goes first and is always present. It is the answer for every handset
on the same network as the phone system, which is most of them, and it is the
one value that cannot be got wrong.
*/
func routingDevices(ctx context.Context, conn pbx, fqdn string) choices {
	out := choices{Labels: map[string]string{}}
	seen := map[string]bool{}
	add := func(value, label string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		out.Values = append(out.Values, value)
		if label != "" && label != value {
			out.Labels[value] = label
		}
	}

	add(fqdn, fqdn+" (the phone system)")

	var answer struct {
		Value []struct {
			Name        string `json:"Name"`
			DisplayName string `json:"DisplayName"`
		} `json:"value"`
	}
	query := url.Values{"$select": {"Name,DisplayName"}, "$top": {"100"}}
	if err := conn.get(ctx, "Sbcs", query, &answer); err != nil {
		// Not worth a raised voice. Most deployments have no SBC, and the
		// phone system itself is already in the list.
		slog.Debug("the phone system would not list its session border controllers", "why", err)
		return out
	}
	for _, s := range answer.Value {
		add(s.Name, s.DisplayName)
	}
	slog.Info("the phone system named its routing devices", "devices", out.Values)
	return out
}
