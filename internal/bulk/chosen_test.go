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
One address across five extensions becomes five addresses.

This is the bug as it was reported: "some fields don't update in bulk, and only
edit the first extension from that bulk list". Setting one address on five is
not an edit that half works — the phone system takes it on the first extension
and refuses the other four, one at a time, after the first has already been
changed.

Refusing it outright was the first fix and the wrong one. "Give these five an
address" is a real thing to want, and the extension number is what tells them
apart, so each gets a tag: reception+101@, reception+102@, and so on. One
address to a mailbox, all delivered to the same place.
*/
func TestOneAddressAcrossManyBecomesOnePerExtension(t *testing.T) {
	want := map[string]Values{}
	for _, n := range []string{"101", "102", "103", "104", "105"} {
		want[n] = Values{"EmailAddress": "reception@example.com"}
	}

	plan := Choose(want, systemWith("101", "102", "103", "104", "105"), forms)

	if plan.Changing != 5 {
		t.Fatalf("%d of 5 would change; every one of them should get an address", plan.Changing)
	}
	seen := map[string]string{}
	for _, row := range plan.Rows {
		if row.Problem != "" {
			t.Fatalf("%s was refused: %s", row.Extension, row.Problem)
		}
		after := row.Changes[0].After
		want := "reception+" + row.Extension + "@example.com"
		if after != want {
			t.Errorf("%s gets %q, want %q", row.Extension, after, want)
		}
		if other, clash := seen[after]; clash {
			t.Errorf("%s and %s would both get %q", other, row.Extension, after)
		}
		seen[after] = row.Extension
	}
}

// The extension that already holds the address keeps it untagged. Tagging it
// would be a change nobody asked for, on the one extension that was already
// right.
func TestTheExtensionThatAlreadyHasItKeepsIt(t *testing.T) {
	now := systemWith("101", "102")
	now["101"]["EmailAddress"] = "reception@example.com"

	plan := Choose(
		map[string]Values{
			"101": {"EmailAddress": "reception@example.com"},
			"102": {"EmailAddress": "reception@example.com"},
		}, now, forms)

	for _, row := range plan.Rows {
		if row.Extension == "101" && len(row.Changes) != 0 {
			t.Errorf("101 already had the address and would be changed to %q", row.Changes[0].After)
		}
		if row.Extension == "102" && row.Changes[0].After != "reception+102@example.com" {
			t.Errorf("102 gets %q", row.Changes[0].After)
		}
	}
}

// An address held by an extension nobody is editing still counts. The phone
// system will refuse it either way, and finding that out from the diff beats
// finding it out from a half-applied batch.
func TestAnAddressHeldElsewhereIsStillTakenj(t *testing.T) {
	now := systemWith("101", "102", "103")
	now["103"]["EmailAddress"] = "reception@example.com"

	plan := Choose(
		map[string]Values{"101": {"EmailAddress": "reception@example.com"}}, now, forms)

	if got := plan.Rows[0].Changes[0].After; got != "reception+101@example.com" {
		t.Errorf("101 gets %q, want it told apart from the one 103 holds", got)
	}
}

// A unique field that is not an address has no rule for telling values apart,
// so the collision is reported rather than guessed at.
func TestAUniqueValueThatIsNotAnAddressIsRefused(t *testing.T) {
	specs := append(append([]Spec{}, forms...),
		Spec{Field: "DeviceTag", Label: "Device tag", Kind: KindText, Group: "General", Unique: true})
	now := systemWith("101", "102")
	now["102"]["DeviceTag"] = "front-desk"

	plan := Choose(map[string]Values{"101": {"DeviceTag": "front-desk"}}, now, specs)

	if plan.Skipped != 1 {
		t.Fatalf("a collision with no way to tell the values apart was not reported")
	}
	if !strings.Contains(plan.Rows[0].Problem, "already has that device tag") {
		t.Errorf("says %q", plan.Rows[0].Problem)
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
