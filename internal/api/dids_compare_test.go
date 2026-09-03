package api

import (
	"testing"

	"github.com/dreulavelle/azir/internal/bulk"
)

/*
What the preview decides about each number.

Pure, and the reason comparePreview takes the phone system's answer as an
argument rather than fetching it: every one of these is a decision somebody
approves, and none of them should need a PBX to check.
*/

// The ordinary import: a carrier's file, half of which the trunk already has.
func TestPreviewSeparatesWhatIsNewFromWhatIsThere(t *testing.T) {
	on := didTrunk{
		ID: 1, Number: "10000", Name: "Voipfone",
		DIDs:   []string{"+15551110000", "+15551110001"},
		Routes: map[string]string{},
	}
	parsed := parsedDIDs(
		did("+15551110000", ""), // already on the trunk
		did("+15551110001", ""), // already on the trunk
		did("+15551110002", ""), // new
		did("+15551110003", ""), // new
	)

	got := comparePreview(parsed, on, []didTrunk{on})

	if n := counts(t, got)["adding"]; n != 2 {
		t.Errorf("would add %d numbers, want 2", n)
	}
	if n := counts(t, got)["already"]; n != 2 {
		t.Errorf("reported %d as already there, want 2", n)
	}
}

/*
A number already on a different trunk is flagged rather than quietly added.

The phone system matches whichever trunk it finds first, so the same DID on two
trunks silently stops working on one of them. This is the only place anybody
would see it before making the second one.
*/
func TestPreviewFlagsANumberThatIsOnAnotherTrunk(t *testing.T) {
	on := didTrunk{ID: 1, Number: "10000", Name: "Voipfone", Routes: map[string]string{}}
	other := didTrunk{
		ID: 2, Number: "10002", Name: "Gamma",
		DIDs:   []string{"+15551110000"},
		Routes: map[string]string{},
	}

	got := comparePreview(parsedDIDs(did("+15551110000", "")), on, []didTrunk{on, other})

	clashes, _ := got["on_other"].([]didChange)
	if len(clashes) != 1 {
		t.Fatalf("flagged %d numbers as being on another trunk, want 1", len(clashes))
	}
	if clashes[0].Existing != "Gamma" {
		t.Errorf("says the number is on %q, want Gamma", clashes[0].Existing)
	}
	// Still counted as being added: the import is what somebody asked for, and
	// this is a warning beside it rather than a refusal.
	if n := counts(t, got)["adding"]; n != 1 {
		t.Errorf("would add %d, want 1 — the clash is a warning, not a refusal", n)
	}
}

// A number on the trunk being imported onto must not report itself as being
// somewhere else.
func TestPreviewDoesNotFlagANumberAgainstItsOwnTrunk(t *testing.T) {
	on := didTrunk{
		ID: 1, Number: "10000",
		DIDs:   []string{"+15551110000"},
		Routes: map[string]string{},
	}

	got := comparePreview(parsedDIDs(did("+15551110000", "")), on, []didTrunk{on})

	if clashes, _ := got["on_other"].([]didChange); len(clashes) != 0 {
		t.Errorf("flagged %v against the trunk it is already on", clashes)
	}
}

/*
A number already assigned elsewhere shows both where it is and where the file
wants it.

This is the row the append-or-replace choice is about, so the screen has to
show both halves of it: appending leaves it where it is, replacing moves it.
Showing only what the file asked for would make appending look like it applied.
*/
func TestPreviewShowsBothSidesOfAnExistingAssignment(t *testing.T) {
	on := didTrunk{
		ID: 1, Number: "10000",
		DIDs:   []string{"+15551110000"},
		Routes: map[string]string{"+15551110000": "205"},
	}

	got := comparePreview(parsedDIDs(did("+15551110000", "101")), on, []didTrunk{on})

	kept, _ := got["already_route"].([]didChange)
	if len(kept) != 1 {
		t.Fatalf("reported %d numbers as already assigned, want 1", len(kept))
	}
	if kept[0].Existing != "205" {
		t.Errorf("says it currently rings %q, want 205", kept[0].Existing)
	}
	if kept[0].Extension != "101" {
		t.Errorf("says the file asked for %q, want 101", kept[0].Extension)
	}
	// Not counted among the ones that would be newly assigned: whether this
	// one moves is the mode's decision, not a foregone conclusion.
	if n := counts(t, got)["routing"]; n != 0 {
		t.Errorf("counted %d as newly assigned, want 0 — this number already has a rule", n)
	}
}

// A DID already ringing the extension the file names is not a conflict worth
// showing, in either mode. It is simply already done.
func TestPreviewIsQuietWhenTheAssignmentAlreadyMatches(t *testing.T) {
	on := didTrunk{
		ID: 1, Number: "10000",
		DIDs:   []string{"+15551110000"},
		Routes: map[string]string{"+15551110000": "101"},
	}

	got := comparePreview(parsedDIDs(did("+15551110000", "101")), on, []didTrunk{on})

	if kept, _ := got["already_route"].([]didChange); len(kept) != 0 {
		t.Errorf("reported %v as a conflict when it already rings there", kept)
	}
}

// What would be newly assigned is what somebody reads before confirming, so it
// counts only the numbers that name an extension and do not already have one.
func TestPreviewCountsWhatWouldActuallyBeAssigned(t *testing.T) {
	on := didTrunk{ID: 1, Number: "10000", Routes: map[string]string{}}
	parsed := parsedDIDs(
		did("+15551110000", "101"), // new, assigned
		did("+15551110001", "102"), // new, assigned
		did("+15551110002", ""),    // new, no extension
	)

	got := comparePreview(parsed, on, []didTrunk{on})

	if n := counts(t, got)["routing"]; n != 2 {
		t.Errorf("counted %d as newly assigned, want 2", n)
	}
	if n := counts(t, got)["adding"]; n != 3 {
		t.Errorf("would add %d, want 3", n)
	}
}

/*
A number already on the trunk with no rule is still an assignment.

The case that made the count wrong the first time: importing a file onto a
trunk that already carries the numbers but has never routed any of them adds
nothing and assigns everything, and a screen saying "0 to add" with no second
figure reads as "this import does nothing".
*/
func TestPreviewCountsAssigningANumberTheTrunkAlreadyHas(t *testing.T) {
	on := didTrunk{
		ID: 1, Number: "10000",
		DIDs:   []string{"+15551110000", "+15551110001"},
		Routes: map[string]string{},
	}
	parsed := parsedDIDs(did("+15551110000", "101"), did("+15551110001", "102"))

	got := comparePreview(parsed, on, []didTrunk{on})
	c := counts(t, got)

	if c["adding"] != 0 {
		t.Errorf("would add %d, want 0 — the trunk already carries both", c["adding"])
	}
	if c["routing"] != 2 {
		t.Errorf("counted %d as newly assigned, want 2", c["routing"])
	}
}

// A number is only ever in one of the two main buckets, so a screen that adds
// them up gets the number of rows it drew.
func TestPreviewPutsEveryNumberInExactlyOneBucket(t *testing.T) {
	on := didTrunk{
		ID: 1, Number: "10000",
		DIDs:   []string{"+15551110000"},
		Routes: map[string]string{"+15551110000": "205"},
	}
	parsed := parsedDIDs(
		did("+15551110000", "101"),
		did("+15551110001", "102"),
		did("+15551110002", ""),
	)

	got := comparePreview(parsed, on, []didTrunk{on})
	c := counts(t, got)

	if c["adding"]+c["already"] != c["total"] {
		t.Errorf("adding %d plus already %d is not the %d numbers read",
			c["adding"], c["already"], c["total"])
	}
}

// Refusals from the file are carried through, so the screen can show which
// lines were not read and why.
func TestPreviewCarriesTheRefusals(t *testing.T) {
	on := didTrunk{ID: 1, Number: "10000", Routes: map[string]string{}}
	parsed := bulk.DIDs{
		Numbers: []bulk.DID{{Number: "+15551110000", Row: 2}},
		Refused: []bulk.DIDProblem{{Raw: "nonsense", Row: 3, Why: "not a phone number"}},
	}

	got := comparePreview(parsed, on, []didTrunk{on})

	if n := counts(t, got)["refused"]; n != 1 {
		t.Errorf("carried %d refusals, want 1", n)
	}
}

func did(number, to string) bulk.DID {
	return bulk.DID{Number: number, Extension: to, Raw: number}
}

func parsedDIDs(numbers ...bulk.DID) bulk.DIDs {
	for i := range numbers {
		numbers[i].Row = i + 2
	}
	return bulk.DIDs{Numbers: numbers}
}

func counts(t *testing.T, preview map[string]any) map[string]int {
	t.Helper()
	got, ok := preview["counts"].(map[string]int)
	if !ok {
		t.Fatalf("the preview has no counts: %v", preview["counts"])
	}
	return got
}
