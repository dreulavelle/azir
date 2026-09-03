package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"

	"github.com/dreulavelle/azir/pkg/plugin"
)

/*
Trunks, and the DID numbers they answer on.

3CX keeps a trunk's DIDs as a plain array of strings on the trunk itself —
Pbx.Trunk.DidNumbers in xapi/openapi.yaml. There is a /DidNumbers collection
as well, and it is read-only: GET is the only verb it has. So the only way to
add a number is to write the trunk's whole array back.

That is the dangerous part of this file and the reason it is written the way it
is. PATCH replaces the array; it does not append to it. A write built from
the sheet alone would take every number already on the trunk off the air, which
on a customer's main trunk is every inbound call they have. So the numbers are
read first, the new ones are merged onto the end, and what goes back always
contains what was already there.

Routing is a second object. A number in the trunk's array makes the phone
system accept a call for it; where that call goes is an inbound rule, created
separately below when the sheet said where. A sheet that did not say is still a
useful import — it is what the carrier hands you — and those numbers are
reported as accepted but unrouted rather than pointed somewhere invented.

Both halves are additive, and that is load-bearing rather than incidental. An
existing number is kept, and an existing rule is never touched: a DID that
already routes somewhere is reported and skipped, because the sheet saying
"101" is somebody adding a number, not somebody asking to repoint a DID that
has been ringing a different desk for two years.
*/

/*
trunk is a trunk as the phone system keeps it.

Note where the name comes from. A trunk has no Name or Host of its own — both
belong to the Gateway it carries, and Pbx.Trunk in the vendored schema has
neither property. Reading them off the trunk compiles, unmarshals to empty
strings and produces a picker of blank rows, which is exactly the kind of guess
schema_test.go exists to catch. It caught this one.
*/
type trunk struct {
	ID        int64    `json:"Id"`
	Number    string   `json:"Number"`
	External  string   `json:"ExternalNumber"`
	Direction string   `json:"Direction"`
	Online    bool     `json:"IsOnline"`
	DIDs      []string `json:"DidNumbers"`
	Gateway   *struct {
		Name string `json:"Name"`
		Host string `json:"Host"`
	} `json:"Gateway"`
}

// name is what to call this trunk on a screen: the provider's name where there
// is one, and the trunk's number where there is not.
func (t trunk) name() string {
	if t.Gateway != nil {
		if name := strings.TrimSpace(t.Gateway.Name); name != "" {
			return name
		}
	}
	return t.Number
}

// host is where the trunk registers, shown beside the name because two trunks
// from one provider are told apart by it and by nothing else.
func (t trunk) host() string {
	if t.Gateway == nil {
		return ""
	}
	return strings.TrimSpace(t.Gateway.Host)
}

/*
listTrunks reports the trunks and the numbers on each.

Asked for plainly, with no $select. This phone system answers 400 to a $select
naming several properties on some collections — the same refusal readGroups
documents — and a trunk list is a handful of rows on any deployment, so asking
for everything costs a slightly larger response and cannot fail that way.

DidNumbers arrives with the entity because it is an ordinary array property
rather than a navigation one, so this is one request rather than one per trunk.
*/
func listTrunks(ctx context.Context, req plugin.Request) (any, error) {
	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	found, err := readTrunks(ctx, conn)
	if err != nil {
		return nil, err
	}

	/*
		Which of each trunk's numbers actually route somewhere.

		Reported alongside the numbers because "40 numbers, 8 of them routed"
		is the state a technician needs to see before importing more, and it is
		the difference between a trunk that works and one that answers every
		call with the same default destination.

		A phone system that will not list its rules is not a failure here. The
		numbers are still the answer to the question this tool was asked, and
		refusing all of them because the routing could not be read would make
		the picker unusable on a system where it happens to be restricted.
	*/
	routes, err := readInboundRules(ctx, conn)
	if err != nil {
		slog.Info("the phone system would not list its inbound rules", "why", err)
		routes = map[int64]map[string]inboundRule{}
	}

	// Which numbers appear on more than one trunk. A DID on two trunks is a
	// real misconfiguration — the phone system matches whichever it finds and
	// the other one silently stops working — and this list is the only place
	// anybody would see it, so it is reported rather than left to be noticed.
	where := map[string][]string{}
	for _, t := range found {
		for _, did := range t.DIDs {
			where[did] = append(where[did], t.Number)
		}
	}

	out := make([]map[string]any, 0, len(found))
	for _, t := range found {
		clashes := []string{}
		for _, did := range t.DIDs {
			if len(where[did]) > 1 {
				clashes = append(clashes, did)
			}
		}
		// Flattened to number -> extension for the screen. The rule ids are
		// the import's business, not a picker's.
		routed := map[string]string{}
		for number, rule := range routes[t.ID] {
			routed[number] = rule.Extension
		}
		out = append(out, map[string]any{
			"id":             t.ID,
			"routes":         routed,
			"routed_count":   len(routed),
			"name":           t.name(),
			"external":       t.External,
			"direction":      t.Direction,
			"number":         t.Number,
			"host":           t.host(),
			"online":         t.Online,
			"dids":           t.DIDs,
			"did_count":      len(t.DIDs),
			"duplicate_dids": clashes,
		})
	}
	return map[string]any{"trunks": out}, nil
}

// readTrunks fetches every trunk with its numbers.
func readTrunks(ctx context.Context, conn pbx) ([]trunk, error) {
	var answer struct {
		Value []trunk `json:"value"`
	}
	if err := conn.get(ctx, "Trunks", nil, &answer); err != nil {
		return nil, err
	}
	sort.SliceStable(answer.Value, func(a, b int) bool {
		return answer.Value[a].Number < answer.Value[b].Number
	})
	return answer.Value, nil
}

// findTrunk resolves what somebody picked — an id or a trunk number — to the
// trunk itself. Both are accepted because the screen has the id and a person
// typing into the assistant has the number.
func findTrunk(ctx context.Context, conn pbx, want string) (trunk, error) {
	want = strings.TrimSpace(want)
	if want == "" {
		return trunk{}, plugin.Errorf("400", "no trunk was named")
	}

	found, err := readTrunks(ctx, conn)
	if err != nil {
		return trunk{}, err
	}
	for _, t := range found {
		if want == t.Number || want == fmt.Sprint(t.ID) {
			return t, nil
		}
	}
	// Only as a last resort, and only on an exact name. Matching loosely here
	// would mean picking a trunk on somebody's behalf and writing numbers to
	// it, which is not a guess worth making.
	for _, t := range found {
		if strings.EqualFold(want, t.name()) {
			return t, nil
		}
	}

	names := make([]string, 0, len(found))
	for _, t := range found {
		names = append(names, t.Number)
	}
	return trunk{}, plugin.Errorf("404",
		"this phone system has no trunk %q; it has %s", want, strings.Join(names, ", "))
}

// didImportArgs is what the import takes: a trunk, the numbers with the
// extension each should ring, and what to do where one is already assigned.
type didImportArgs struct {
	Trunk string `json:"trunk"`
	Mode  string `json:"mode"`
	DIDs  []struct {
		Number string `json:"number"`
		// Extension is a plain number. What it turns out to be — a person, a
		// queue, a ring group, a digital receptionist — is resolved against
		// the phone system, because 3CX numbers all of them out of one plan
		// and the number therefore says which one it is on its own.
		Extension string `json:"extension"`
	} `json:"dids"`
}

/*
What an import does with a DID that is already assigned somewhere.

Two answers, because both are things people actually want and neither is safe
to assume. Appending is the default: a business that has bought a second number
for the sales desk wants it ringing alongside the first, and an import that
silently repointed the first would take a working number off a working desk.
Replacing is the deliberate one: the number moved, and the old assignment is
meant to go.

The distinction only ever matters for a DID that already has a rule. A number
with no rule is created either way, and neither mode ever removes a number from
a trunk.
*/
const (
	modeAppend  = "append"
	modeReplace = "replace"
)

/*
importTrunkDIDs adds numbers to a trunk and routes the ones the sheet placed.

Read, merge, write. The read is not an optimisation and skipping it is not a
shortcut: PATCH replaces the array, so the read is what stops this from
deleting every number the trunk already had.

A number already present is reported rather than written twice, which is what
makes running the same file again safe. That matters more than it sounds:
re-importing after fixing three bad rows is the normal way this gets used.

The numbers go on before any rule does. A rule naming a DID the trunk does not
carry is a rule that never fires, and doing it in this order means a run that
fails halfway has left a trunk that answers on numbers rather than rules that
point at nothing.
*/
func importTrunkDIDs(ctx context.Context, req plugin.Request) (any, error) {
	var args didImportArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return nil, plugin.Errorf("400", "arguments could not be parsed")
	}
	if len(args.DIDs) == 0 {
		return nil, plugin.Errorf("400", "no numbers were given")
	}
	if len(args.DIDs) > maxImportDIDs {
		return nil, plugin.Errorf("400", "that is more than %d numbers; do it in smaller batches", maxImportDIDs)
	}

	// Refused rather than defaulted. A mode nobody recognises is a caller that
	// believes something about this import, and picking one of the two on
	// their behalf is picking whether a live DID gets repointed.
	mode := strings.ToLower(strings.TrimSpace(args.Mode))
	switch mode {
	case "":
		mode = modeAppend
	case modeAppend, modeReplace:
	default:
		return nil, plugin.Errorf("400", "mode is %q; it is %q or %q", args.Mode, modeAppend, modeReplace)
	}

	wanted := make([]wantedDID, 0, len(args.DIDs))
	for _, did := range args.DIDs {
		number := strings.TrimSpace(did.Number)
		if number == "" {
			continue
		}
		wanted = append(wanted, wantedDID{
			Number: number, Extension: strings.TrimSpace(did.Extension),
		})
	}
	if len(wanted) == 0 {
		return nil, plugin.Errorf("400", "no numbers were given")
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}
	on, err := findTrunk(ctx, conn, args.Trunk)
	if err != nil {
		return nil, err
	}

	merged, added, already := mergeDIDs(on.DIDs, wanted)
	if len(added) > 0 {
		if err := conn.patch(ctx, fmt.Sprintf("Trunks(%d)", on.ID), map[string]any{
			"DidNumbers": merged,
		}); err != nil {
			return nil, err
		}
	}

	routed, leftAlone, unrouted, failed := routeDIDs(ctx, conn, on, wanted, mode)

	return map[string]any{
		"trunk":          on.Number,
		"added":          added,
		"already":        already,
		"total":          len(merged),
		"routed":         routed,
		"already_routed": leftAlone,
		"unrouted":       unrouted,
		"failed":         failed,
		"mode":           mode,
		"summary": importSummary(on.Number, len(merged),
			added, already, routed, leftAlone, unrouted, failed),
	}, nil
}

// wantedDID is one number the sheet asked for, and the extension it should
// ring. Extension is a plain number; what it is gets resolved at import.
type wantedDID struct {
	Number    string
	Extension string
}

/*
routeDIDs assigns each number the sheet placed to the extension it named.

A number with no extension is left alone and reported: it is on the trunk and
the phone system will answer it, but where the call goes is whatever the trunk
already does by default.

A number that already has a rule is where the two modes differ. Appending
leaves it exactly as it is and says so; replacing repoints it. Nothing here
ever deletes a rule — replacing rewrites the one rule that DID already had,
which is reversible from the console and visible in the activity log.
*/
func routeDIDs(ctx context.Context, conn pbx, on trunk, wanted []wantedDID, mode string) (
	routed []map[string]string, leftAlone []map[string]string,
	unrouted []string, failed map[string]string,
) {
	routed, leftAlone = []map[string]string{}, []map[string]string{}
	unrouted, failed = []string{}, map[string]string{}

	// Nothing to resolve or read if the sheet placed nothing. A single-column
	// import should not cost a request nobody needs.
	placing := false
	for _, did := range wanted {
		if did.Extension != "" {
			placing = true
			break
		}
	}
	if !placing {
		for _, did := range wanted {
			unrouted = append(unrouted, did.Number)
		}
		return routed, leftAlone, unrouted, failed
	}

	// Everything the phone system will let a DID point at, and what each of
	// those numbers actually is. Asked once for the whole system rather than
	// once per row: a sheet of two hundred numbers pointing at eight
	// extensions would otherwise be two hundred lookups.
	peers, err := readPeers(ctx, conn)
	if err != nil {
		for _, did := range wanted {
			if did.Extension == "" {
				unrouted = append(unrouted, did.Number)
				continue
			}
			failed[did.Number] = "could not read the phone system's extensions: " + err.Error()
		}
		return routed, leftAlone, unrouted, failed
	}

	byTrunk, err := readInboundRules(ctx, conn)
	if err != nil {
		// Without the current rules there is no way to tell a new DID from one
		// already answering somewhere, and creating a second rule for a live
		// number is how a business's calls start going to the wrong desk. So
		// nothing is assigned.
		for _, did := range wanted {
			if did.Extension == "" {
				unrouted = append(unrouted, did.Number)
				continue
			}
			failed[did.Number] = "could not read the existing inbound rules: " + err.Error()
		}
		return routed, leftAlone, unrouted, failed
	}
	existing := byTrunk[on.ID]
	if existing == nil {
		// This trunk has no DID rules at all, which is the ordinary state of a
		// trunk somebody is importing numbers onto. A map rather than nil so
		// the lookups below stay lookups.
		existing = map[string]inboundRule{}
	}

	for _, did := range wanted {
		if did.Extension == "" {
			unrouted = append(unrouted, did.Number)
			continue
		}

		to, known := peers[did.Extension]
		if !known {
			failed[did.Number] = fmt.Sprintf("this phone system has no %s", did.Extension)
			continue
		}
		if !canTakeACall(to.Type) {
			// Named rather than described. "805 is a Parking" is something a
			// technician can act on; "that cannot take a call" is not.
			failed[did.Number] = fmt.Sprintf("%s is a %s, which a DID cannot ring", did.Extension, to.Type)
			continue
		}

		was, taken := existing[did.Number]
		switch {
		case taken && was.Extension == did.Extension:
			// Already exactly what the sheet asks for. Not a change in either
			// mode, and not worth reporting as one.
			leftAlone = append(leftAlone, map[string]string{
				"number": did.Number, "extension": was.Extension, "why": "already assigned there",
			})
		case taken && mode != modeReplace:
			leftAlone = append(leftAlone, map[string]string{
				"number": did.Number, "extension": was.Extension, "why": "already assigned",
			})
		case taken:
			if err := conn.patch(ctx, fmt.Sprintf("InboundRules(%d)", was.ID), map[string]any{
				"OfficeHoursDestination": destinationFor(to),
			}); err != nil {
				failed[did.Number] = err.Error()
				continue
			}
			existing[did.Number] = inboundRule{ID: was.ID, Extension: did.Extension}
			routed = append(routed, map[string]string{
				"number": did.Number, "extension": did.Extension, "was": was.Extension,
			})
		default:
			if err := createInboundRule(ctx, conn, on, did, to); err != nil {
				failed[did.Number] = err.Error()
				continue
			}
			// Recorded as taken so a sheet naming the same number twice in two
			// rows cannot produce two rules for it.
			existing[did.Number] = inboundRule{Extension: did.Extension}
			routed = append(routed, map[string]string{
				"number": did.Number, "extension": did.Extension,
			})
		}
	}
	return routed, leftAlone, unrouted, failed
}

// peer is one number on the phone system, and what it is.
type peer struct {
	ID     int64  `json:"Id"`
	Number string `json:"Number"`
	Name   string `json:"Name"`
	Type   string `json:"Type"`
}

/*
readPeers is every number the phone system has, by number.

This is what makes the sheet a single column of numbers rather than a column of
numbers with a type written beside each. 3CX numbers users, queues, ring
groups and digital receptionists out of one plan, so a number identifies what
it points at without help — and asking a person to say it again is asking them
to say it wrong on the row where it matters.
*/
func readPeers(ctx context.Context, conn pbx) (map[string]peer, error) {
	var answer struct {
		Value []peer `json:"value"`
	}
	if err := conn.get(ctx, "Peers", nil, &answer); err != nil {
		return nil, err
	}

	out := make(map[string]peer, len(answer.Value))
	for _, p := range answer.Value {
		number := strings.TrimSpace(p.Number)
		if number == "" {
			continue
		}
		out[number] = p
	}
	return out, nil
}

/*
canTakeACall reports whether a DID may point at this kind of number.

3CX's PeerType and DestinationType overlap but are not the same list: a parking
spot and a conference room have numbers and are not somewhere an inbound call
can be sent. Checked here so the refusal names the number, rather than arriving
as a 400 from the phone system halfway through a batch.
*/
func canTakeACall(peerType string) bool {
	switch peerType {
	case "Extension", "Queue", "RingGroup", "IVR", "Fax", "RoutePoint":
		return true
	}
	return false
}

// destinationFor writes a resolved number as the destination the phone system
// takes. PeerType and DestinationType spell these six the same way, which
// canTakeACall is what guarantees.
func destinationFor(to peer) map[string]any {
	return map[string]any{"To": to.Type, "Number": to.Number, "External": ""}
}

// inboundRule is one existing rule: which rule it is, and where it points.
type inboundRule struct {
	ID int64
	// Extension is the destination's number, so it compares directly against
	// what a sheet asked for.
	Extension string
}

/*
readInboundRules reports which DIDs already point somewhere, by trunk.

Keyed by trunk because a rule on another trunk matching the same digits is a
different route for a different carrier, and treating it as this number's rule
would leave a DID unassigned with nothing to say why.

One request for the whole phone system rather than one per trunk. Both callers
want the same answer — the picker, to say how many of a trunk's numbers are
actually assigned, and the import, to know which ones are already taken — and
fetching it twice in two shapes is how the two come to disagree.
*/
func readInboundRules(ctx context.Context, conn pbx) (map[int64]map[string]inboundRule, error) {
	var answer struct {
		Value []struct {
			ID        int64  `json:"Id"`
			Condition string `json:"Condition"`
			Data      string `json:"Data"`
			TrunkDN   *struct {
				ID     int64  `json:"Id"`
				Number string `json:"Number"`
			} `json:"TrunkDN"`
			OfficeHoursDestination *struct {
				To     string `json:"To"`
				Number string `json:"Number"`
			} `json:"OfficeHoursDestination"`
		} `json:"value"`
	}
	q := url.Values{}
	q.Set("$expand", "TrunkDN")
	if err := conn.get(ctx, "InboundRules", q, &answer); err != nil {
		return nil, err
	}

	out := map[int64]map[string]inboundRule{}
	for _, rule := range answer.Value {
		if rule.TrunkDN == nil {
			continue
		}
		if !strings.EqualFold(rule.Condition, conditionDID) {
			continue
		}
		number := strings.TrimSpace(rule.Data)
		if number == "" {
			continue
		}
		to := ""
		if rule.OfficeHoursDestination != nil {
			to = strings.TrimSpace(rule.OfficeHoursDestination.Number)
		}
		if out[rule.TrunkDN.ID] == nil {
			out[rule.TrunkDN.ID] = map[string]inboundRule{}
		}
		out[rule.TrunkDN.ID][number] = inboundRule{ID: rule.ID, Extension: to}
	}
	return out, nil
}

// conditionDID is the inbound-rule condition that matches on the dialled
// number. Named rather than written at each use, and checked against the
// schema by schema_test.go along with everything else this file reads.
const conditionDID = "BasedOnDID"

/*
createInboundRule points one DID at one number.

The office-hours destination is set and the two "alter during" switches are
turned off, so the rule sends the call to the same place at every hour of every
day. A sheet that says a number rings extension 101 says exactly that; it does
not say what should happen at seven in the evening, and inventing an
out-of-hours destination from silence would be this file deciding something
nobody asked it to.
*/
func createInboundRule(ctx context.Context, conn pbx, on trunk, did wantedDID, to peer) error {
	body := map[string]any{
		"RuleName":                               ruleName(did.Number),
		"Condition":                              conditionDID,
		"Data":                                   did.Number,
		"TrunkDN":                                map[string]any{"Id": on.ID, "Number": on.Number},
		"OfficeHoursDestination":                 destinationFor(to),
		"AlterDestinationDuringOutOfOfficeHours": false,
		"AlterDestinationDuringHolidays":         false,
	}
	return conn.post(ctx, "InboundRules", body, nil)
}

// ruleName is what the rule is called in the 3CX console. The number, because
// that is what somebody scanning a list of two hundred rules is looking for.
func ruleName(number string) string {
	return "DID " + number
}

// importSummary says what happened in words, because a screen and a log both
// read this and deriving it twice is how the two come to disagree.
func importSummary(
	trunkNumber string, total int,
	added []string, already []string,
	routed []map[string]string, leftAlone []map[string]string,
	unrouted []string, failed map[string]string,
) string {
	var said []string
	switch {
	case len(added) > 0:
		said = append(said, fmt.Sprintf("added %d numbers to %s, which now answers on %d",
			len(added), trunkNumber, total))
	default:
		said = append(said, fmt.Sprintf("%s already carried every one of those numbers", trunkNumber))
	}
	if len(already) > 0 && len(added) > 0 {
		said = append(said, fmt.Sprintf("%d were already there", len(already)))
	}
	// Assignments split by what actually happened to them, because "routed 12"
	// reads the same whether twelve numbers were given a desk or twelve live
	// numbers were moved off one.
	made, moved := 0, 0
	for _, one := range routed {
		if one["was"] != "" {
			moved++
			continue
		}
		made++
	}
	if made > 0 {
		said = append(said, fmt.Sprintf("assigned %d of them", made))
	}
	if moved > 0 {
		said = append(said, fmt.Sprintf("moved %d to a different extension", moved))
	}
	if len(leftAlone) > 0 {
		said = append(said, fmt.Sprintf("%d were already assigned and were left as they were", len(leftAlone)))
	}
	if len(unrouted) > 0 {
		said = append(said, fmt.Sprintf("%d have no extension yet, so their calls go wherever the trunk sends them by default",
			len(unrouted)))
	}
	if len(failed) > 0 {
		said = append(said, fmt.Sprintf("%d could not be assigned", len(failed)))
	}
	return strings.Join(said, "; ") + "."
}

// maxImportDIDs bounds one call. Matched to internal/bulk's own limit, which is
// what produces the list on the way in.
const maxImportDIDs = 5000

/*
mergeDIDs adds what is missing and keeps everything that was there.

Order is preserved and existing entries come first, so a trunk's own list does
not get shuffled by an import — a diff of somebody's PBX configuration should
show the numbers that were added and nothing else.

Comparison is exact. Two numbers that differ by a country code are two
different numbers to a phone system matching what a carrier sent, and treating
them as one here would be this file deciding something internal/bulk
deliberately refuses to decide.
*/
func mergeDIDs(existing []string, wanted []wantedDID) (merged []string, added []string, already []string) {
	merged = make([]string, 0, len(existing)+len(wanted))
	have := make(map[string]bool, len(existing))
	for _, did := range existing {
		merged = append(merged, did)
		have[did] = true
	}

	added, already = []string{}, []string{}
	for _, did := range wanted {
		number := strings.TrimSpace(did.Number)
		if number == "" {
			continue
		}
		if have[number] {
			already = append(already, number)
			continue
		}
		have[number] = true
		merged = append(merged, number)
		added = append(added, number)
	}
	return merged, added, already
}
