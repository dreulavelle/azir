package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/dreulavelle/azir/pkg/plugin"
)

/*
Creating things on a phone system, rather than only changing them.

A ticket asking for ten new extensions and a ring group is the ordinary shape
of this work, and until now it ended with somebody opening the PBX console.

Everything here reports per item. A run of ten creates is ten independent
outcomes, and collapsing them into one success or one failure hides the case
that actually happens: eight made, two skipped because the numbers were already
taken. A caller — person or model — needs to know which two.
*/

type newExtension struct {
	Number    string `json:"number"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Email     string `json:"email"`
}

type createExtensionsArgs struct {
	Extensions []newExtension `json:"extensions"`
	StartAt    string         `json:"start_at"`
	Count      int            `json:"count"`
	NamePrefix string         `json:"name_prefix"`
}

// howManyAtOnce bounds one call. A slip in a count field should not be able to
// fill somebody's dial plan.
const howManyAtOnce = 50

func createExtensions(ctx context.Context, req plugin.Request) (any, error) {
	var args createExtensionsArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, plugin.Errorf("400", "those arguments could not be read")
		}
	}

	wanted, err := args.resolve()
	if err != nil {
		return nil, err
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	// Which numbers exist already, asked once rather than per extension. A
	// create that collides is refused by the PBX anyway, but finding out here
	// means the answer says "taken" instead of relaying a vendor error.
	taken, err := existingNumbers(ctx, conn)
	if err != nil {
		return nil, err
	}

	results := make([]map[string]any, 0, len(wanted))
	created := 0
	for _, ext := range wanted {
		if taken[ext.Number] {
			results = append(results, map[string]any{
				"extension": ext.Number, "created": false, "reason": "that extension already exists",
			})
			continue
		}

		// Only properties the PBX's own schema defines. OData refuses a body
		// carrying anything it does not recognise, and it refuses it by failing
		// to bind at all — which surfaces as "the delta field is required",
		// naming a field that appears nowhere in the schema and sending you
		// looking for an envelope that was never the problem.
		body := map[string]any{
			"Number":    ext.Number,
			"FirstName": ext.FirstName,
			"LastName":  ext.LastName,
			"AuthID":    ext.Number,
			"Enabled":   true,
		}
		if ext.Email != "" {
			body["EmailAddress"] = ext.Email
		}

		if err := conn.post(ctx, "Users", body, nil); err != nil {
			results = append(results, map[string]any{
				"extension": ext.Number, "created": false, "reason": plainReason(err),
			})
			continue
		}
		taken[ext.Number] = true
		created++
		results = append(results, map[string]any{"extension": ext.Number, "created": true})
	}

	return map[string]any{
		"created":  created,
		"asked":    len(wanted),
		"results":  results,
		"complete": created == len(wanted),
	}, nil
}

// resolve turns either way of asking into one list of extensions to create.
func (a createExtensionsArgs) resolve() ([]newExtension, error) {
	if len(a.Extensions) > 0 && a.Count > 0 {
		return nil, plugin.Errorf("400",
			"name the extensions or give a starting number and a count, not both")
	}

	if len(a.Extensions) > 0 {
		if len(a.Extensions) > howManyAtOnce {
			return nil, plugin.Errorf("400", "that is more than %d at once", howManyAtOnce)
		}
		for i, e := range a.Extensions {
			if strings.TrimSpace(e.Number) == "" {
				return nil, plugin.Errorf("400", "extension %d has no number", i+1)
			}
		}
		return a.Extensions, nil
	}

	if a.Count <= 0 {
		return nil, plugin.Errorf("400", "nothing to create: give a list of extensions, or a starting number and a count")
	}
	if a.Count > howManyAtOnce {
		return nil, plugin.Errorf("400", "that is more than %d at once", howManyAtOnce)
	}

	start, err := strconv.Atoi(strings.TrimSpace(a.StartAt))
	if err != nil {
		return nil, plugin.Errorf("400", "%q is not a number to start from", a.StartAt)
	}

	// Sequential from the starting number. Numbers already in use are reported
	// as skipped rather than silently stepped over, because a caller asking for
	// ten from 200 means 200-209 and should be told if they did not get it.
	out := make([]newExtension, 0, a.Count)
	for i := range a.Count {
		number := strconv.Itoa(start + i)
		name := strings.TrimSpace(a.NamePrefix)
		if name != "" {
			name += " " + number
		}
		out = append(out, newExtension{Number: number, FirstName: name})
	}
	return out, nil
}

// existingNumbers is every extension the PBX already has.
func existingNumbers(ctx context.Context, conn pbx) (map[string]bool, error) {
	taken := map[string]bool{}
	for skip := 0; skip < maxExtensions; skip += pageSize {
		q := url.Values{}
		q.Set("$select", "Number")
		q.Set("$top", fmt.Sprint(pageSize))
		q.Set("$skip", fmt.Sprint(skip))

		var page struct {
			Value []struct {
				Number string `json:"Number"`
			} `json:"value"`
		}
		if err := conn.get(ctx, "Users", q, &page); err != nil {
			return nil, err
		}
		for _, u := range page.Value {
			taken[u.Number] = true
		}
		if len(page.Value) < pageSize {
			break
		}
	}
	return taken, nil
}

// --- removing ---------------------------------------------------------------

type deleteArgs struct {
	Extensions []string `json:"extensions"`
}

/*
deleteExtensions removes extensions, named one at a time.

There is deliberately no way to say "all", no pattern and no range. Every other
destructive shortcut on this plugin exists because somebody would otherwise do
it by hand a hundred times; this one has no such excuse, and the failure mode is
a phone system with nobody able to answer it.

It is still four gates from anybody: the caller holds phone.manage, an
administrator has enabled writes for this plugin, the tool is approved, and the
assistant cannot reach it at all — a model can only ever propose it for a person
to look at.
*/
func deleteExtensions(ctx context.Context, req plugin.Request) (any, error) {
	var args deleteArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, plugin.Errorf("400", "those arguments could not be read")
		}
	}
	if len(args.Extensions) == 0 {
		return nil, plugin.Errorf("400", "name the extensions to remove")
	}
	if len(args.Extensions) > howManyAtOnce {
		return nil, plugin.Errorf("400", "that is more than %d at once", howManyAtOnce)
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}
	known, err := extensionIDs(ctx, conn)
	if err != nil {
		return nil, err
	}

	results := make([]map[string]any, 0, len(args.Extensions))
	removed := 0
	for _, number := range args.Extensions {
		number = strings.TrimSpace(number)
		id, ok := known[number]
		if !ok {
			results = append(results, map[string]any{
				"extension": number, "removed": false, "reason": "no extension here has that number",
			})
			continue
		}
		if err := conn.remove(ctx, fmt.Sprintf("Users(%d)", id)); err != nil {
			results = append(results, map[string]any{
				"extension": number, "removed": false, "reason": plainReason(err),
			})
			continue
		}
		removed++
		results = append(results, map[string]any{"extension": number, "removed": true})
	}

	return map[string]any{"removed": removed, "asked": len(args.Extensions), "results": results}, nil
}

// --- ring groups -------------------------------------------------------------

func listRingGroups(ctx context.Context, req plugin.Request) (any, error) {
	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("$top", fmt.Sprint(pageSize))
	// Members is a navigation property, so it arrives empty unless asked for by
	// name. No $select here: a ring group carries nothing sensitive, unlike an
	// extension, where the field list is the security boundary.
	q.Set("$expand", "Members")

	var page struct {
		Value []struct {
			ID       int64  `json:"Id"`
			Number   string `json:"Number"`
			Name     string `json:"Name"`
			Strategy string `json:"RingStrategy"`
			RingTime int    `json:"RingTime"`
			Members  []struct {
				Number string `json:"Number"`
				Name   string `json:"Name"`
			} `json:"Members"`
		} `json:"value"`
	}
	if err := conn.get(ctx, "Ringgroups", q, &page); err != nil {
		return nil, err
	}

	groups := make([]map[string]any, 0, len(page.Value))
	for _, g := range page.Value {
		members := make([]map[string]any, 0, len(g.Members))
		for _, m := range g.Members {
			members = append(members, map[string]any{"extension": m.Number, "name": m.Name})
		}
		groups = append(groups, map[string]any{
			"number":       g.Number,
			"name":         g.Name,
			"strategy":     g.Strategy,
			"ring_seconds": g.RingTime,
			"members":      members,
		})
	}
	return map[string]any{"ring_groups": groups, "total": len(groups)}, nil
}

type ringGroupArgs struct {
	Number      string   `json:"number"`
	Name        string   `json:"name"`
	Members     []string `json:"members"`
	Strategy    string   `json:"strategy"`
	RingSeconds int      `json:"ring_seconds"`
}

func createRingGroup(ctx context.Context, req plugin.Request) (any, error) {
	var args ringGroupArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, plugin.Errorf("400", "those arguments could not be read")
		}
	}
	if strings.TrimSpace(args.Number) == "" || strings.TrimSpace(args.Name) == "" {
		return nil, plugin.Errorf("400", "a ring group needs a number and a name")
	}
	if len(args.Members) == 0 {
		return nil, plugin.Errorf("400", "a ring group with nobody in it would never ring")
	}

	strategy := args.Strategy
	if strategy == "" {
		strategy = "RingAll"
	}
	seconds := args.RingSeconds
	if seconds <= 0 {
		seconds = 30
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	// Members are sent as references to existing extensions, so a typo names a
	// number that is not there rather than quietly creating an empty group.
	known, err := extensionIDs(ctx, conn)
	if err != nil {
		return nil, err
	}
	members := make([]map[string]any, 0, len(args.Members))
	var missing []string
	for _, number := range args.Members {
		id, ok := known[strings.TrimSpace(number)]
		if !ok {
			missing = append(missing, number)
			continue
		}
		members = append(members, map[string]any{"Id": id, "Number": strings.TrimSpace(number)})
	}
	if len(missing) > 0 {
		return nil, plugin.Errorf("400",
			"no extension here is numbered %s", strings.Join(missing, ", "))
	}

	body := map[string]any{
		"Number":       strings.TrimSpace(args.Number),
		"Name":         strings.TrimSpace(args.Name),
		"RingStrategy": strategy,
		"RingTime":     seconds,
		"Members":      members,
	}

	var made struct {
		ID int64 `json:"Id"`
	}
	if err := conn.post(ctx, "Ringgroups", body, &made); err != nil {
		return nil, err
	}

	return map[string]any{
		"created":      true,
		"number":       args.Number,
		"name":         args.Name,
		"strategy":     strategy,
		"ring_seconds": seconds,
		"members":      args.Members,
	}, nil
}

// deleteRingGroup removes a ring group by its number.
func deleteRingGroup(ctx context.Context, req plugin.Request) (any, error) {
	var args struct {
		Number string `json:"number"`
	}
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, plugin.Errorf("400", "those arguments could not be read")
		}
	}
	number := strings.TrimSpace(args.Number)
	if number == "" {
		return nil, plugin.Errorf("400", "name the ring group to remove, by its number")
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("$select", "Id,Number")
	q.Set("$top", fmt.Sprint(pageSize))
	var page struct {
		Value []struct {
			ID     int64  `json:"Id"`
			Number string `json:"Number"`
		} `json:"value"`
	}
	if err := conn.get(ctx, "Ringgroups", q, &page); err != nil {
		return nil, err
	}
	for _, g := range page.Value {
		if g.Number == number {
			if err := conn.remove(ctx, fmt.Sprintf("Ringgroups(%d)", g.ID)); err != nil {
				return nil, err
			}
			return map[string]any{"removed": true, "number": number}, nil
		}
	}
	return nil, plugin.Errorf("404", "no ring group here is numbered %s", number)
}

// extensionIDs maps an extension number to the PBX's internal id.
func extensionIDs(ctx context.Context, conn pbx) (map[string]int64, error) {
	out := map[string]int64{}
	for skip := 0; skip < maxExtensions; skip += pageSize {
		q := url.Values{}
		q.Set("$select", "Id,Number")
		q.Set("$top", fmt.Sprint(pageSize))
		q.Set("$skip", fmt.Sprint(skip))

		var page struct {
			Value []struct {
				ID     int64  `json:"Id"`
				Number string `json:"Number"`
			} `json:"value"`
		}
		if err := conn.get(ctx, "Users", q, &page); err != nil {
			return nil, err
		}
		for _, u := range page.Value {
			out[u.Number] = u.ID
		}
		if len(page.Value) < pageSize {
			break
		}
	}
	return out, nil
}

// plainReason strips the plugin error wrapper so a per-item result reads as a
// sentence rather than as a status code.
func plainReason(err error) string {
	msg := err.Error()
	if _, after, found := strings.Cut(msg, ": "); found {
		return after
	}
	return msg
}
