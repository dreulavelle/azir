package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

/*
The property this whole file exists to keep: an import never removes a number.

3CX takes a trunk's DIDs as one array and PATCH replaces it, so an import built
from the sheet alone would take every number already on the trunk off the air.
On a customer's main trunk that is every inbound call they have. If this test
ever fails, the merge has stopped being a merge.
*/
func TestMergeKeepsEveryNumberAlreadyOnTheTrunk(t *testing.T) {
	existing := []string{"+15551110000", "+15551110001", "+15551110002"}
	merged, added, already := mergeDIDs(existing, want("+15552220000", "+15552220001"))

	for _, number := range existing {
		if !slices.Contains(merged, number) {
			t.Fatalf("%s was on the trunk and is not in what would be written back: %v", number, merged)
		}
	}
	if len(merged) != 5 {
		t.Errorf("would write %d numbers, want 5: %v", len(merged), merged)
	}
	if len(added) != 2 {
		t.Errorf("added %v, want the two new ones", added)
	}
	if len(already) != 0 {
		t.Errorf("reported %v as already there, want none", already)
	}
}

// The existing numbers keep their order and stay at the front, so a diff of a
// customer's configuration shows what was added and nothing else.
func TestMergeDoesNotReorderWhatWasThere(t *testing.T) {
	existing := []string{"+15551110002", "+15551110000", "+15551110001"}
	merged, _, _ := mergeDIDs(existing, want("+15552220000"))

	for i, number := range existing {
		if merged[i] != number {
			t.Fatalf("position %d is %s, want %s — the trunk's own order was shuffled", i, merged[i], number)
		}
	}
}

// Running the same file twice is normal — it is how somebody re-imports after
// fixing three bad rows — and the second run must change nothing.
func TestMergeIsSafeToRunTwice(t *testing.T) {
	first, _, _ := mergeDIDs([]string{"+15551110000"}, want("+15552220000", "+15552220001"))
	second, added, already := mergeDIDs(first, want("+15552220000", "+15552220001"))

	if len(second) != len(first) {
		t.Errorf("a second run would write %d numbers, want the same %d", len(second), len(first))
	}
	if len(added) != 0 {
		t.Errorf("a second run would add %v, want nothing", added)
	}
	if len(already) != 2 {
		t.Errorf("a second run reported %d as already there, want 2", len(already))
	}
}

/*
Two numbers differing only by a country code are two different numbers.

The phone system matches what the carrier actually sent, so treating these as
one here would be this file deciding something internal/bulk deliberately
refuses to decide.
*/
func TestMergeTreatsACountryCodeAsPartOfTheNumber(t *testing.T) {
	merged, added, _ := mergeDIDs([]string{"+15551110000"}, want("5551110000"))
	if len(added) != 1 {
		t.Fatalf("added %v, want the number without the country code treated as new", added)
	}
	if len(merged) != 2 {
		t.Errorf("would write %v, want both forms kept", merged)
	}
}

/*
Appending leaves a DID that is already assigned exactly as it is.

A business that buys a second number for the sales desk wants it ringing
alongside the first. An import that silently repointed the first would take a
working number off a working desk, so appending is the default and it never
touches an existing assignment.
*/
func TestAppendingLeavesAnAssignedDIDAlone(t *testing.T) {
	conn, seen := fakePBX(t, existingRule(7, "+15551110000", "205"))

	routed, leftAlone, _, failed := routeDIDs(context.Background(), conn,
		trunk{ID: 1, Number: "10000"}, []wantedDID{
			{Number: "+15551110000", Extension: "101"},
			{Number: "+15551110001", Extension: "102"},
		}, modeAppend)

	if len(failed) != 0 {
		t.Fatalf("assigning failed: %v", failed)
	}
	if len(leftAlone) != 1 || leftAlone[0]["number"] != "+15551110000" {
		t.Fatalf("left alone %v, want the number that was already assigned", leftAlone)
	}
	if leftAlone[0]["extension"] != "205" {
		t.Errorf("reported the existing extension as %q, want 205", leftAlone[0]["extension"])
	}
	if len(routed) != 1 || routed[0]["number"] != "+15551110001" {
		t.Fatalf("assigned %v, want only the number that had none", routed)
	}
	if len(seen.patches()) != 0 {
		t.Errorf("appending rewrote an existing rule: %v", seen.patches())
	}
	if len(seen.creates()) != 1 {
		t.Fatalf("created %d rules, want 1", len(seen.creates()))
	}
}

/*
Replacing repoints a DID that is already assigned.

The deliberate one: the number moved, and the old assignment is meant to go.
The existing rule is rewritten rather than a second one created, because a DID
with two rules is a DID whose behaviour depends on which the phone system finds
first.
*/
func TestReplacingRepointsAnAssignedDID(t *testing.T) {
	conn, seen := fakePBX(t, existingRule(7, "+15551110000", "205"))

	routed, leftAlone, _, failed := routeDIDs(context.Background(), conn,
		trunk{ID: 1, Number: "10000"},
		[]wantedDID{{Number: "+15551110000", Extension: "101"}}, modeReplace)

	if len(failed) != 0 {
		t.Fatalf("assigning failed: %v", failed)
	}
	if len(leftAlone) != 0 {
		t.Errorf("left %v alone in replace mode", leftAlone)
	}
	if len(routed) != 1 {
		t.Fatalf("assigned %v, want the one number", routed)
	}
	// Reported with what it was, so the activity log and the screen can both
	// say a live number moved rather than that one was assigned.
	if routed[0]["was"] != "205" {
		t.Errorf("reported the previous extension as %q, want 205", routed[0]["was"])
	}

	if len(seen.creates()) != 0 {
		t.Errorf("replacing created a second rule for a DID that had one: %v", seen.creates())
	}
	patches := seen.patches()
	if len(patches) != 1 {
		t.Fatalf("patched %d rules, want 1", len(patches))
	}
	if !strings.Contains(patches[0]["_path"].(string), "InboundRules(7)") {
		t.Errorf("patched %v, want the existing rule 7", patches[0]["_path"])
	}
	to, _ := patches[0]["OfficeHoursDestination"].(map[string]any)
	if to == nil || to["Number"] != "101" || to["To"] != "Extension" {
		t.Errorf("repointed at %v, want extension 101", to)
	}
}

// A DID already assigned to the extension the sheet names is not a change in
// either mode. Rewriting it would be a write nobody asked for.
func TestAnAssignmentThatAlreadyMatchesIsNotAChange(t *testing.T) {
	for _, mode := range []string{modeAppend, modeReplace} {
		t.Run(mode, func(t *testing.T) {
			conn, seen := fakePBX(t, existingRule(7, "+15551110000", "101"))

			routed, _, _, failed := routeDIDs(context.Background(), conn,
				trunk{ID: 1, Number: "10000"},
				[]wantedDID{{Number: "+15551110000", Extension: "101"}}, mode)

			if len(failed) != 0 {
				t.Fatalf("assigning failed: %v", failed)
			}
			if len(routed) != 0 {
				t.Errorf("reported %v as a change when it already points there", routed)
			}
			if len(seen.creates())+len(seen.patches()) != 0 {
				t.Error("wrote to the phone system for an assignment that already matched")
			}
		})
	}
}

/*
Several DIDs may ring one extension.

This is what appending is for, and it has to work in one pass: a business with
three numbers on the sales desk has three rules pointing at one extension, and
nothing here should treat the second and third as conflicts.
*/
func TestSeveralNumbersMayRingOneExtension(t *testing.T) {
	conn, seen := fakePBX(t, noRules())

	routed, _, _, failed := routeDIDs(context.Background(), conn,
		trunk{ID: 1, Number: "10000"}, []wantedDID{
			{Number: "+15551110000", Extension: "101"},
			{Number: "+15551110001", Extension: "101"},
			{Number: "+15551110002", Extension: "101"},
		}, modeAppend)

	if len(failed) != 0 {
		t.Fatalf("assigning failed: %v", failed)
	}
	if len(routed) != 3 {
		t.Fatalf("assigned %d numbers, want all 3", len(routed))
	}
	if len(seen.creates()) != 3 {
		t.Errorf("created %d rules, want 3", len(seen.creates()))
	}
}

/*
What a number is gets resolved against the phone system, not written in the
sheet.

3CX numbers people, queues, ring groups and digital receptionists out of one
plan, so the number says which one it is. This is the test that the sheet never
has to say.
*/
func TestWhatANumberIsComesFromThePhoneSystem(t *testing.T) {
	conn, seen := fakePBX(t, noRules())

	_, _, _, failed := routeDIDs(context.Background(), conn,
		trunk{ID: 1, Number: "10000"}, []wantedDID{
			{Number: "+15551110000", Extension: "101"},
			{Number: "+15551110001", Extension: "800"},
			{Number: "+15551110002", Extension: "801"},
			{Number: "+15551110003", Extension: "900"},
		}, modeAppend)

	if len(failed) != 0 {
		t.Fatalf("assigning failed: %v", failed)
	}

	want := map[string]string{
		"+15551110000": "Extension",
		"+15551110001": "Queue",
		"+15551110002": "RingGroup",
		"+15551110003": "IVR",
	}
	for _, rule := range seen.creates() {
		to, _ := rule["OfficeHoursDestination"].(map[string]any)
		if to == nil {
			t.Fatalf("a rule was created with no destination: %v", rule)
		}
		number, _ := rule["Data"].(string)
		if got := to["To"]; got != want[number] {
			t.Errorf("%s was sent to a %v, want a %s", number, got, want[number])
		}
	}
}

// A number the phone system does not have is refused by name, rather than sent
// and rejected halfway through a batch.
func TestANumberThatDoesNotExistIsRefusedByName(t *testing.T) {
	conn, seen := fakePBX(t, noRules())

	_, _, _, failed := routeDIDs(context.Background(), conn,
		trunk{ID: 1, Number: "10000"},
		[]wantedDID{{Number: "+15551110000", Extension: "999"}}, modeAppend)

	if len(failed) != 1 {
		t.Fatalf("failed %d, want 1", len(failed))
	}
	if !strings.Contains(failed["+15551110000"], "999") {
		t.Errorf("the refusal reads %q and does not name the number", failed["+15551110000"])
	}
	if len(seen.creates()) != 0 {
		t.Error("a rule was created for an extension that does not exist")
	}
}

// A number that exists and cannot take a call — a parking spot — is refused
// with what it actually is, because that is what a technician can act on.
func TestANumberThatCannotTakeACallIsRefused(t *testing.T) {
	conn, seen := fakePBX(t, noRules())

	_, _, _, failed := routeDIDs(context.Background(), conn,
		trunk{ID: 1, Number: "10000"},
		[]wantedDID{{Number: "+15551110000", Extension: "805"}}, modeAppend)

	if len(failed) != 1 {
		t.Fatalf("failed %d, want 1", len(failed))
	}
	if !strings.Contains(failed["+15551110000"], "Parking") {
		t.Errorf("the refusal reads %q and does not say what 805 is", failed["+15551110000"])
	}
	if len(seen.creates()) != 0 {
		t.Error("a rule was created pointing at a parking spot")
	}
}

// A rule is created for the trunk it was imported onto, matching on the
// dialled number.
func TestACreatedRuleNamesItsTrunkAndItsNumber(t *testing.T) {
	conn, seen := fakePBX(t, noRules())

	_, _, _, failed := routeDIDs(context.Background(), conn,
		trunk{ID: 42, Number: "10042"},
		[]wantedDID{{Number: "+15551110000", Extension: "800"}}, modeAppend)
	if len(failed) != 0 {
		t.Fatalf("assigning failed: %v", failed)
	}

	made := seen.creates()
	if len(made) != 1 {
		t.Fatalf("created %d rules, want 1", len(made))
	}
	rule := made[0]
	if rule["Condition"] != conditionDID {
		t.Errorf("condition is %v, want %s", rule["Condition"], conditionDID)
	}
	if rule["Data"] != "+15551110000" {
		t.Errorf("the rule matches %v, want the DID", rule["Data"])
	}
	trunkDN, _ := rule["TrunkDN"].(map[string]any)
	if trunkDN == nil || trunkDN["Id"] != float64(42) {
		t.Errorf("the rule names trunk %v, want 42", trunkDN)
	}
	// The destination applies at every hour, because a sheet saying which
	// extension a number rings did not say anything about evenings.
	if rule["AlterDestinationDuringOutOfOfficeHours"] != false {
		t.Error("the rule alters its destination out of hours, which the sheet never asked for")
	}
}

// A number with no extension is added to the trunk and left unassigned, which
// is the single-column import.
func TestANumberWithNoExtensionIsNotAssigned(t *testing.T) {
	conn, seen := fakePBX(t, noRules())

	routed, _, unrouted, failed := routeDIDs(context.Background(), conn,
		trunk{ID: 1, Number: "10000"}, []wantedDID{{Number: "+15551110000"}}, modeAppend)

	if len(failed) != 0 {
		t.Fatalf("assigning failed: %v", failed)
	}
	if len(routed) != 0 {
		t.Errorf("assigned %v, want nothing", routed)
	}
	if len(unrouted) != 1 || unrouted[0] != "+15551110000" {
		t.Errorf("reported %v as unassigned, want the one number", unrouted)
	}
	if len(seen.creates()) != 0 {
		t.Error("a rule was created for a number the sheet gave no extension for")
	}
	// And nothing was read at all: a single-column import should not cost a
	// request nobody needs.
	if seen.reads() != 0 {
		t.Errorf("made %d reads for an import that assigns nothing", seen.reads())
	}
}

/*
When the existing rules cannot be read, nothing is assigned.

Without them there is no way to tell a DID with no rule from one already
ringing somewhere, and creating a second rule for a live number is how a
business's calls start going to the wrong desk. Failing loudly is the only safe
answer.
*/
func TestNothingIsAssignedWhenTheExistingRulesCannotBeRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "Peers") {
			w.Header().Set("content-type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{
				{"Id": 11, "Number": "101", "Type": "Extension"},
				{"Id": 12, "Number": "102", "Type": "Extension"},
			}})
			return
		}
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		t.Errorf("a rule was written after the existing ones could not be read: %s", r.URL.Path)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(server.Close)

	routed, _, _, failed := routeDIDs(context.Background(),
		pbx{base: server.URL, token: "test"}, trunk{ID: 1, Number: "10000"},
		[]wantedDID{
			{Number: "+15551110000", Extension: "101"},
			{Number: "+15551110001", Extension: "102"},
		}, modeAppend)

	if len(routed) != 0 {
		t.Errorf("assigned %v after failing to read the existing rules", routed)
	}
	if len(failed) != 2 {
		t.Errorf("reported %d failures, want both numbers", len(failed))
	}
}

// A rule on a different trunk is not this number's rule. Skipping a DID because
// another carrier's trunk matches the same digits would leave it unassigned
// with nothing to say why.
func TestARuleOnAnotherTrunkDoesNotCount(t *testing.T) {
	conn, seen := fakePBX(t, map[string]any{
		"value": []map[string]any{{
			"Id":                     7,
			"Condition":              conditionDID,
			"Data":                   "+15551110000",
			"TrunkDN":                map[string]any{"Id": 99, "Number": "10099"},
			"OfficeHoursDestination": map[string]any{"To": "Extension", "Number": "205"},
		}},
	})

	routed, leftAlone, _, failed := routeDIDs(context.Background(), conn,
		trunk{ID: 1, Number: "10000"},
		[]wantedDID{{Number: "+15551110000", Extension: "101"}}, modeAppend)

	if len(failed) != 0 {
		t.Fatalf("assigning failed: %v", failed)
	}
	if len(leftAlone) != 0 {
		t.Errorf("left %v alone because another trunk matched the same digits", leftAlone)
	}
	if len(routed) != 1 {
		t.Fatalf("assigned %v, want the one number", routed)
	}
	if len(seen.creates()) != 1 {
		t.Error("no rule was created for a number whose only rule is on another trunk")
	}
}

// A sheet naming the same number twice must not produce two rules for it.
func TestOneNumberTwiceMakesOneRule(t *testing.T) {
	conn, seen := fakePBX(t, noRules())

	routed, leftAlone, _, failed := routeDIDs(context.Background(), conn,
		trunk{ID: 1, Number: "10000"}, []wantedDID{
			{Number: "+15551110000", Extension: "101"},
			{Number: "+15551110000", Extension: "102"},
		}, modeAppend)

	if len(failed) != 0 {
		t.Fatalf("assigning failed: %v", failed)
	}
	if len(seen.creates()) != 1 {
		t.Fatalf("created %d rules for one number", len(seen.creates()))
	}
	if len(routed) != 1 {
		t.Errorf("assigned %v, want one", routed)
	}
	if len(leftAlone) != 1 {
		t.Errorf("the repeat was reported as %v, want it left alone", leftAlone)
	}
}

// noRules is a phone system with no inbound rules at all, which is the
// ordinary state of a trunk somebody is importing numbers onto.
func noRules() map[string]any {
	return map[string]any{"value": []map[string]any{}}
}

// existingRule is one DID already pointing at an extension on trunk 1.
func existingRule(id int, did string, to string) map[string]any {
	return map[string]any{"value": []map[string]any{{
		"Id":                     id,
		"RuleName":               "DID " + did,
		"Condition":              conditionDID,
		"Data":                   did,
		"TrunkDN":                map[string]any{"Id": 1, "Number": "10000"},
		"OfficeHoursDestination": map[string]any{"To": "Extension", "Number": to},
	}}}
}

// want turns bare numbers into what the import takes, for the merge tests that
// do not care about assignment.
func want(numbers ...string) []wantedDID {
	out := make([]wantedDID, 0, len(numbers))
	for _, number := range numbers {
		out = append(out, wantedDID{Number: number})
	}
	return out
}

// watched records what a fake phone system was asked to do.
type watched struct {
	created []map[string]any
	patched []map[string]any
	gets    int
}

func (w *watched) creates() []map[string]any { return w.created }
func (w *watched) patches() []map[string]any { return w.patched }
func (w *watched) reads() int                { return w.gets }

/*
fakePBX answers the two reads an import makes and records every write.

The numbering plan it reports is the one the tests reason about: 101 and 102
are people, 800 is a queue, 801 a ring group, 900 a digital receptionist, and
805 is a parking spot — which has a number and is not somewhere a call can be
sent.
*/
func fakePBX(t *testing.T, rules map[string]any) (pbx, *watched) {
	t.Helper()
	seen := &watched{}

	peers := map[string]any{"value": []map[string]any{
		{"Id": 11, "Number": "101", "Name": "Reception", "Type": "Extension"},
		{"Id": 12, "Number": "102", "Name": "Sales", "Type": "Extension"},
		{"Id": 13, "Number": "205", "Name": "Accounts", "Type": "Extension"},
		{"Id": 21, "Number": "800", "Name": "Support queue", "Type": "Queue"},
		{"Id": 22, "Number": "801", "Name": "Everyone", "Type": "RingGroup"},
		{"Id": 23, "Number": "900", "Name": "Main menu", "Type": "IVR"},
		{"Id": 24, "Number": "805", "Name": "Park 1", "Type": "Parking"},
	}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "InboundRules"):
			seen.gets++
			w.Header().Set("content-type", "application/json")
			_ = json.NewEncoder(w).Encode(rules)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "Peers"):
			seen.gets++
			w.Header().Set("content-type", "application/json")
			_ = json.NewEncoder(w).Encode(peers)
		case r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var rule map[string]any
			if err := json.Unmarshal(body, &rule); err != nil {
				t.Errorf("a rule was posted that is not json: %v", err)
			}
			seen.created = append(seen.created, rule)
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(rule)
		case r.Method == http.MethodPatch:
			body, _ := io.ReadAll(r.Body)
			var change map[string]any
			if err := json.Unmarshal(body, &change); err != nil {
				t.Errorf("a rule was patched with something that is not json: %v", err)
			}
			change["_path"] = r.URL.Path
			seen.patched = append(seen.patched, change)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s of %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	return pbx{base: server.URL, token: "test"}, seen
}
