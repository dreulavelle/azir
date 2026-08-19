package bulk

import (
	"strings"
	"testing"
)

/*
Two things a bulk edit made from the console has to get right, and did not.

Both were found the same way: by somebody using the screen and reporting that
it did not do what they asked. Neither failed. One silently did nothing and
said so as "unchanged"; the other silently did it to one extension out of five.
*/

var forms = []Spec{
	{Field: FieldName, Label: "Display name", Kind: KindText, Group: "General"},
	{Field: "Mobile", Label: "Mobile number", Kind: KindText, Group: "General"},
	{Field: "EmailAddress", Label: "Email", Kind: KindText, Group: "General", Unique: true},
	{Field: "RecordCalls", Label: "Record calls", Kind: KindBool, Group: "Options"},
	{Field: "SRTPMode", Label: "SRTP", Kind: KindChoice, Group: "Options",
		Choices: []string{"SRTPDisabled", "SRTPEnabled"}},
}

func systemWith(numbers ...string) Current {
	now := Current{}
	for _, n := range numbers {
		now[n] = Values{
			FieldName:      "Person " + n,
			"Mobile":       "5550000",
			"EmailAddress": n + "@example.com",
			"RecordCalls":  "no",
			"SRTPMode":     "SRTPDisabled",
		}
	}
	return now
}

/*
Emptying a field is a change.

A blank cell in a spreadsheet means "this column was not filled in", and
treating it as "set this to nothing" would wipe a field on every row of a sheet
with an unused column. That rule is right there and was copied here, where it
is wrong: a form sends the fields somebody set and nothing else, so a field
arriving empty is somebody having cleared it.

The result was that a mobile number, once set, could never be removed. The
screen reported "unchanged", which is the part that makes it worth a test:
nothing failed, and the answer was wrong.
*/
func TestClearingAFieldIsAChange(t *testing.T) {
	plan := Choose(
		map[string]Values{"100": {"Mobile": ""}},
		systemWith("100"),
		forms,
	)

	if plan.Changing != 1 {
		t.Fatalf("clearing a field read as %d changes and %d unchanged", plan.Changing, plan.Unchanged)
	}
	change := plan.Rows[0].Changes[0]
	if change.Before != "5550000" || change.After != "" {
		t.Errorf("the before-and-after says %q -> %q", change.Before, change.After)
	}
}

// Only for fields that can hold nothing. A picker cannot produce an empty
// choice or an empty yes-or-no, so an empty one of those is still the absence
// of an answer and has to stay "leave it alone".
func TestAnEmptyChoiceStillMeansLeaveItAlone(t *testing.T) {
	plan := Choose(
		map[string]Values{"100": {"SRTPMode": "", "RecordCalls": ""}},
		systemWith("100"),
		forms,
	)
	if plan.Changing != 0 {
		t.Errorf("an empty picker was read as a change: %+v", plan.Rows[0].Changes)
	}
}

/*
One email address across five extensions is refused before anything is written.

This is the bug as it was reported: "some fields don't update in bulk, and only
edit the first extension from that bulk list". Setting one address on five is
not an edit that half works — the phone system takes it on the first extension
and refuses the other four, one at a time, after the first has already been
changed. What somebody sees is one extension edited and four errors, and there
is no undoing the first from there.

So it never starts. The rows carry the reason instead.
*/
func TestOneUniqueValueAcrossManyIsRefused(t *testing.T) {
	want := map[string]Values{}
	for _, n := range []string{"101", "102", "103", "104", "105"} {
		want[n] = Values{"EmailAddress": "reception@example.com"}
	}

	plan := Choose(want, systemWith("101", "102", "103", "104", "105"), forms)

	if plan.Changing != 0 {
		t.Errorf("%d extensions would have been changed; the first would have taken the address "+
			"and the rest would have been refused", plan.Changing)
	}
	if plan.Skipped != 5 {
		t.Fatalf("%d of 5 rows were marked, want all of them", plan.Skipped)
	}
	for _, row := range plan.Rows {
		if !strings.Contains(row.Problem, "different on every extension") {
			t.Errorf("%s says %q, which does not explain why", row.Extension, row.Problem)
		}
	}
}

// Giving each extension its own address is an ordinary bulk edit and stays one.
func TestDifferentUniqueValuesAreFine(t *testing.T) {
	plan := Choose(
		map[string]Values{
			"101": {"EmailAddress": "ann@example.com"},
			"102": {"EmailAddress": "bob@example.com"},
		},
		systemWith("101", "102"),
		forms,
	)
	if plan.Changing != 2 {
		t.Errorf("%d of 2 would change; giving everybody their own address is the normal case", plan.Changing)
	}
}

// Two spellings of one address are one address. A phone system that will not
// take the same one twice will not take two cases of it either.
func TestUniquenessIgnoresCase(t *testing.T) {
	plan := Choose(
		map[string]Values{
			"101": {"EmailAddress": "Reception@Example.com"},
			"102": {"EmailAddress": "reception@example.com"},
		},
		systemWith("101", "102"),
		forms,
	)
	if plan.Changing != 0 {
		t.Errorf("%d would change, but both are the same address", plan.Changing)
	}
}

// Setting a unique field on one extension is not a clash with anything.
func TestOneExtensionIsNeverAClash(t *testing.T) {
	plan := Choose(
		map[string]Values{"101": {"EmailAddress": "ann@example.com"}},
		systemWith("101"),
		forms,
	)
	if plan.Changing != 1 {
		t.Errorf("renaming one extension's address was refused: %+v", plan.Rows)
	}
}

// A field nobody marked unique is not guarded. A shared on-call mobile is a
// real thing and the phone system takes it.
func TestOnlyUniqueFieldsAreGuarded(t *testing.T) {
	plan := Choose(
		map[string]Values{
			"101": {"Mobile": "5551234"},
			"102": {"Mobile": "5551234"},
		},
		systemWith("101", "102"),
		forms,
	)
	if plan.Changing != 2 {
		t.Errorf("%d of 2 would change; a shared mobile is allowed", plan.Changing)
	}
}
