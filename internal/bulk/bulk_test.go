package bulk

import (
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
)

// Sheets arrive from whatever somebody had open, which is rarely a clean
// comma-separated file written by a program.
func TestSheetsAreReadAsExported(t *testing.T) {
	cases := []struct{ name, text string }{
		{"commas", "Extension,Name,Active\n101,Alice,yes\n"},
		{"semicolons, as European Excel writes", "Extension;Name;Active\n101;Alice;yes\n"},
		{"tabs", "Extension\tName\tActive\n101\tAlice\tyes\n"},
		{"a byte-order mark, as Excel writes", "\ufeffExtension,Name,Active\n101,Alice,yes\n"},
		{"blank lines in the middle", "Extension,Name,Active\n101,Alice,yes\n\n\n"},
		{"ragged rows", "Extension,Name,Active\n101,Alice\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sheet, err := Parse(strings.NewReader(c.text))
			if err != nil {
				t.Fatal(err)
			}
			// The mark must not become part of the first column's name, or the
			// header reads correctly everywhere except where it is compared.
			if sheet.Columns[0] != "Extension" {
				t.Errorf("first column is %q", sheet.Columns[0])
			}
			if len(sheet.Rows) != 1 {
				t.Fatalf("%d rows", len(sheet.Rows))
			}
			if sheet.Cell(0, 0) != "101" {
				t.Errorf("extension read as %q", sheet.Cell(0, 0))
			}
			// A short row is empty at the end, not an index panic.
			_ = sheet.Cell(0, 9)
		})
	}
}

func TestAnUnusableFileSaysWhy(t *testing.T) {
	for _, c := range []struct{ name, text string }{
		{"empty", ""},
		{"header only", "Extension,Name\n"},
		{"whitespace", "   \n  \n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(c.text)); err == nil {
				t.Error("accepted a file with nothing in it")
			}
		})
	}
}

func sheet(t *testing.T, text string) Sheet {
	t.Helper()
	s, err := Parse(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

/*
What is applied is the difference from what the system says now.

A sheet exported last Tuesday and edited since describes a system that has
moved on. Applying it wholesale would quietly undo whatever changed in
between; comparing first means those rows appear as changes somebody can see
and decline.
*/
func TestOnlyTheDifferenceIsProposed(t *testing.T) {
	s := sheet(t, "Extension,Name,Active\n101,Alice Smith,yes\n102,Bob Jones,no\n103,Carol,yes\n")
	now := Current{
		"101": {FieldName: "A. Smith", FieldEnabled: "yes"},  // name differs
		"102": {FieldName: "Bob Jones", FieldEnabled: "yes"}, // enabled differs
		"103": {FieldName: "Carol", FieldEnabled: "yes"},     // nothing differs
	}

	plan, err := Build(s, Mapping{Extension: 0, Fields: map[Field]int{FieldName: 1, FieldEnabled: 2}}, now, Core)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Changing != 2 || plan.Unchanged != 1 || plan.Skipped != 0 {
		t.Fatalf("changing %d, unchanged %d, skipped %d", plan.Changing, plan.Unchanged, plan.Skipped)
	}

	by := map[string]Row{}
	for _, r := range plan.Rows {
		by[r.Extension] = r
	}

	if got := by["101"].Changes; len(got) != 1 || got[0].Before != "A. Smith" || got[0].After != "Alice Smith" {
		t.Errorf("101: %+v", got)
	}
	if got := by["102"].Changes; len(got) != 1 || got[0].Before != "yes" || got[0].After != "no" {
		t.Errorf("102: %+v", got)
	}
	if len(by["103"].Changes) != 0 {
		t.Errorf("103 changed when it matched: %+v", by["103"].Changes)
	}
}

/*
An empty cell leaves a field alone.

A sheet with a blank column would otherwise wipe that field on every extension
in it. This flow exists to catch that sort of accident at the approval step —
not causing it is better than catching it.
*/
func TestABlankCellChangesNothing(t *testing.T) {
	s := sheet(t, "Extension,Name,Active\n101,,\n")
	now := Current{"101": {FieldName: "Alice", FieldEnabled: "yes"}}

	plan, err := Build(s, Mapping{Extension: 0, Fields: map[Field]int{FieldName: 1, FieldEnabled: 2}}, now, Core)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Changing != 0 || plan.Unchanged != 1 {
		t.Errorf("a blank row proposed something: %+v", plan.Rows)
	}
}

/*
Rows the system has never heard of are shown, not dropped.

A sheet naming extensions that do not exist usually means the wrong customer or
a column shifted by one. Applying the rows that happened to match and saying
nothing about the rest would be the worst of both.
*/
func TestRowsThatCannotBeAppliedAreShown(t *testing.T) {
	s := sheet(t, "Extension,Name\n101,Alice\n999,Ghost\n,Nameless\n101,Duplicate\n")
	now := Current{"101": {FieldName: "A. Smith"}}

	plan, err := Build(s, Mapping{Extension: 0, Fields: map[Field]int{FieldName: 1}}, now, Core)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Rows) != 4 {
		t.Fatalf("dropped a row: %d of 4", len(plan.Rows))
	}
	if plan.Changing != 1 || plan.Skipped != 3 {
		t.Fatalf("changing %d, skipped %d", plan.Changing, plan.Skipped)
	}

	problems := map[string]string{}
	for _, r := range plan.Rows {
		if r.Problem != "" {
			problems[r.Extension+"/"+r.Name] = r.Problem
		}
	}
	if len(problems) != 3 {
		t.Errorf("problems: %v", problems)
	}
	// Rows that do something sort first, because that is the order somebody
	// reviews them in.
	if !plan.Rows[0].Changed() {
		t.Error("a row that changes nothing came first")
	}
}

// A value that is not a yes or a no stops that row rather than being guessed
// at. Guessing here disables somebody's phone.
func TestAnUnreadableYesOrNoStopsItsRow(t *testing.T) {
	s := sheet(t, "Extension,Active\n101,perhaps\n")
	now := Current{"101": {FieldName: "Alice", FieldEnabled: "yes"}}

	plan, err := Build(s, Mapping{Extension: 0, Fields: map[Field]int{FieldEnabled: 1}}, now, Core)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Skipped != 1 || plan.Changing != 0 {
		t.Fatalf("%+v", plan.Rows)
	}
	if !strings.Contains(plan.Rows[0].Problem, "perhaps") {
		t.Errorf("the problem does not say what was wrong: %q", plan.Rows[0].Problem)
	}
}

// The many ways a sheet says yes, because somebody's export decides this and
// they do not get a say.
func TestTheWaysASheetSaysYes(t *testing.T) {
	for _, yes := range []string{"yes", "Y", "TRUE", "1", "on", "Enabled", "active"} {
		if got, err := truth(yes); err != nil || !got {
			t.Errorf("%q read as %v (%v)", yes, got, err)
		}
	}
	for _, no := range []string{"no", "N", "FALSE", "0", "off", "Disabled", "inactive"} {
		if got, err := truth(no); err != nil || got {
			t.Errorf("%q read as %v (%v)", no, got, err)
		}
	}
}

// A mapping that names no column, or a column that is not there, is refused
// before anything is compared.
func TestAMappingMustNameRealColumns(t *testing.T) {
	s := sheet(t, "Extension,Name\n101,Alice\n")
	for _, m := range []Mapping{
		{Extension: -1, Fields: map[Field]int{FieldName: 1}},
		{Extension: 9, Fields: map[Field]int{FieldName: 1}},
		{Extension: 0, Fields: map[Field]int{}},
		{Extension: 0, Fields: map[Field]int{FieldName: 9}},
	} {
		if _, err := Build(s, m, Current{}, Core); err == nil {
			t.Errorf("accepted %+v", m)
		}
	}
}

// The headers people actually put on these sheets, so nobody maps four columns
// by hand every time. A wrong guess costs one click in a dropdown.
func TestColumnsAreGuessedFromTheirHeaders(t *testing.T) {
	for _, header := range [][]string{
		{"Extension", "Name", "Enabled"},
		{"ext", "display name", "active"},
		{"Extension Number", "Full Name", "Status"},
		{"EXT_NUMBER", "USER-NAME", "IN.USE"},
	} {
		m := Suggest(header, Core)
		if m.Extension != 0 {
			t.Errorf("%v: extension guessed as %d", header, m.Extension)
		}
		if m.Fields[FieldName] != 1 {
			t.Errorf("%v: name guessed as %d", header, m.Fields[FieldName])
		}
		if m.Fields[FieldEnabled] != 2 {
			t.Errorf("%v: enabled guessed as %d", header, m.Fields[FieldEnabled])
		}
	}

	// A header it does not recognise is left for a person, not guessed at.
	m := Suggest([]string{"Widget", "Sprocket"}, Core)
	if m.Extension != -1 || len(m.Fields) != 0 {
		t.Errorf("guessed at columns it should not have: %+v", m)
	}
}

/*
Creating is off unless it was asked for, and that default is the safety.

A sheet uploaded against the wrong customer matches nothing. With creating off
that is a screen full of harmless "no such extension" rows; with it on it is
twenty extensions on somebody else's phone system. The same sheet, the same
click, and the difference is one setting made while deciding what the sheet
means.
*/
func TestNothingIsCreatedUnlessItWasAskedFor(t *testing.T) {
	s := sheet(t, "Extension,Name\n101,Alice\n900,New Person\n")
	now := Current{"101": {FieldName: "Alice"}}

	off, err := Build(s, Mapping{Extension: 0, Fields: map[Field]int{FieldName: 1}}, now, Core)
	if err != nil {
		t.Fatal(err)
	}
	if off.Creating != 0 || off.Skipped != 1 {
		t.Fatalf("created something with creating off: %+v", off)
	}

	on, err := Build(s, Mapping{Extension: 0, Fields: map[Field]int{FieldName: 1}, Create: true}, now, Core)
	if err != nil {
		t.Fatal(err)
	}
	if on.Creating != 1 || on.Skipped != 0 {
		t.Fatalf("%+v", on)
	}
	for _, r := range on.Rows {
		if r.Creates() && (r.Extension != "900" || r.Wanted != "New Person") {
			t.Errorf("a number the sheet asked for was not the number given: %+v", r)
		}
	}
}

/*
A row with no number gets the next one after the highest, never a gap.

A gap is usually an extension somebody deleted, and its number can still carry
the DID routing and voicemail that went with it — so reusing it means a caller
dialling the old number reaches a new person.
*/
func TestANewExtensionStartsAfterTheHighest(t *testing.T) {
	// 102 is missing, and must stay missing.
	s := sheet(t, "Extension,Name\n,First Hire\n,Second Hire\n")
	now := Current{"100": {FieldName: "A"}, "101": {FieldName: "B"}, "103": {FieldName: "C"}}

	plan, err := Build(s, Mapping{Extension: 0, Fields: map[Field]int{FieldName: 1}, Create: true}, now, Core)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Creating != 2 {
		t.Fatalf("%+v", plan)
	}

	var given []string
	for _, r := range plan.Rows {
		if r.Creates() {
			given = append(given, r.Extension)
		}
	}
	if len(given) != 2 || given[0] != "104" || given[1] != "105" {
		t.Errorf("assigned %v, wanted 104 and 105 — 102 is a gap and must stay one", given)
	}
}

// A new extension with no name is refused rather than created as a number
// nobody can identify.
func TestANewExtensionNeedsAName(t *testing.T) {
	s := sheet(t, "Extension,Name\n900,\n")
	plan, err := Build(s, Mapping{Extension: 0, Fields: map[Field]int{FieldName: 1}, Create: true}, Current{"100": {FieldName: "A"}}, Core)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Creating != 0 || plan.Skipped != 1 {
		t.Errorf("created a nameless extension: %+v", plan.Rows)
	}
}

// Edits and creations are counted apart and sorted apart, because a plan that
// buries twenty new extensions among unchanged rows is a plan nobody reads.
func TestEditsAndCreationsAreShownApart(t *testing.T) {
	s := sheet(t, "Extension,Name\n100,Renamed\n101,Same\n900,Brand New\n")
	now := Current{"100": {FieldName: "Old"}, "101": {FieldName: "Same"}}

	plan, err := Build(s, Mapping{Extension: 0, Fields: map[Field]int{FieldName: 1}, Create: true}, now, Core)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Changing != 1 || plan.Creating != 1 || plan.Unchanged != 1 {
		t.Fatalf("changing %d, creating %d, unchanged %d", plan.Changing, plan.Creating, plan.Unchanged)
	}
	if !plan.Rows[0].Changed() {
		t.Error("edits do not come first")
	}
	if !plan.Rows[1].Creates() {
		t.Error("creations do not follow the edits")
	}
}

/*
The sheet Azir writes is one Azir can read back.

Three lists have to agree for the round trip to work: the headers written into
the starting sheet, the words Suggest looks for, and the column numbers
CanonicalMapping assumes. Nothing else compares them, and when they drift the
failure is quiet — the download works, the upload works, and the mapping
dropdowns come back empty with no reason given.

This is the third bug of this exact shape in this codebase. The other two were
permission labels and audit action names.
*/
func TestAzirCanReadTheSheetItWrites(t *testing.T) {
	columns := SheetColumns(Core)
	guessed := Suggest(columns, Core)
	want := CanonicalMapping(Core)

	if guessed.Extension != want.Extension {
		t.Errorf("Suggest put the extension column at %d, CanonicalMapping says %d",
			guessed.Extension, want.Extension)
	}
	for field, col := range want.Fields {
		got, mapped := guessed.Fields[field]
		if !mapped {
			t.Errorf("Suggest does not recognise %q, the header Azir writes for %s",
				columns[col], field)
			continue
		}
		if got != col {
			t.Errorf("Suggest put %s at column %d, CanonicalMapping says %d", field, got, col)
		}
	}
	if len(guessed.Fields) != len(want.Fields) {
		t.Errorf("Suggest mapped %d fields, CanonicalMapping has %d",
			len(guessed.Fields), len(want.Fields))
	}
}

// Line writes one cell per spec; a mismatch would silently shift every column
// after it.
func TestLineFillsEveryColumn(t *testing.T) {
	line := Line("100", Values{FieldName: "Reception", FieldEnabled: "yes"}, Core)
	if len(line) != len(SheetColumns(Core)) {
		t.Fatalf("Line wrote %d cells for %d columns", len(line), len(SheetColumns(Core)))
	}
	for i, cell := range line {
		if cell == "" {
			t.Errorf("Line left %q empty, so a field is missing from its switch",
				SheetColumns(Core)[i])
		}
	}
}

// A sheet Azir wrote, parsed back, is the same sheet. The BOM and delimiter
// handling in Parse make this less obvious than it sounds.
func TestTheRoundTripSurvivesParsing(t *testing.T) {
	var out strings.Builder
	sheet := csv.NewWriter(&out)
	if err := sheet.Write(SheetColumns(Core)); err != nil {
		t.Fatal(err)
	}
	if err := sheet.Write(Line("100", Values{FieldName: "Dreu Lavelle", FieldEnabled: "yes"}, Core)); err != nil {
		t.Fatal(err)
	}
	if err := sheet.Write(Line("101", Values{FieldName: "Front Desk", FieldEnabled: "no"}, Core)); err != nil {
		t.Fatal(err)
	}
	sheet.Flush()

	parsed, err := Parse(strings.NewReader(out.String()))
	if err != nil {
		t.Fatalf("Azir could not parse its own sheet: %v", err)
	}

	now := Current{
		"100": {FieldName: "Dreu Lavelle", FieldEnabled: "yes"},
		"101": {FieldName: "Front Desk", FieldEnabled: "no"},
	}
	plan, err := Build(parsed, Suggest(parsed.Columns, Core), now, Core)
	if err != nil {
		t.Fatalf("building from Azir's own sheet: %v", err)
	}
	// Downloaded and uploaded unedited, nothing should change.
	if plan.Changing != 0 || plan.Skipped != 0 {
		t.Errorf("an unedited round trip proposed %d changes and skipped %d; want none of either",
			plan.Changing, plan.Skipped)
	}
	if plan.Unchanged != 2 {
		t.Errorf("unchanged = %d, want 2", plan.Unchanged)
	}
}

/*
A sheet with no rows encodes as an empty list, not as null.

The undo of a sheet that only created extensions has nothing to put back, so
its sheet is genuinely empty. Encoded as null it reached the console as a value
nothing could iterate, and the screen listing recent sheets crashed on a row
already in the database — a crash no amount of testing the endpoints would have
found, because the JSON was well-formed and the failure was in reading it.
*/
func TestEmptySheetsAndPlansEncodeAsLists(t *testing.T) {
	sheet, err := json.Marshal(Sheet{Columns: SheetColumns(Core)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sheet), `"rows":[]`) {
		t.Errorf("an empty sheet encoded as %s; want rows as []", sheet)
	}

	plan, err := json.Marshal(Plan{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plan), `"rows":[]`) {
		t.Errorf("an empty plan encoded as %s; want rows as []", plan)
	}

	// And the round trip still works, so the local-type trick has not quietly
	// dropped a field.
	full, err := json.Marshal(Sheet{Columns: []string{"Extension"}, Rows: [][]string{{"100"}}})
	if err != nil {
		t.Fatal(err)
	}
	var back Sheet
	if err := json.Unmarshal(full, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Rows) != 1 || back.Cell(0, 0) != "100" {
		t.Errorf("round trip lost the rows: %s -> %+v", full, back)
	}
}

/*
A phone system's own fields become columns, and come back through the sheet.

The sheet used to carry two fields on a phone system that will set thirty-two,
because this package held its own list. It builds from what the plugin
publishes now, and this is the whole round trip: publish, write a sheet,
read it back, compare.
*/
func TestPublishedFieldsMakeItThroughTheSheet(t *testing.T) {
	published := []Spec{
		{Field: "Enabled", Label: "Enabled", Kind: KindBool, Group: "Basics"},
		{Field: "RecordCalls", Label: "Record calls", Kind: KindBool, Group: "Recording"},
		{Field: "SRTPMode", Label: "Encrypt the audio", Kind: KindChoice, Group: "Network",
			Choices: []string{"SRTPDisabled", "SRTPEnabled"}},
	}
	specs := Merge(Core, published)

	// 3CX publishes its own Enabled, which is the switch Core already
	// describes. Two columns fighting over one setting is the bug.
	if len(specs) != 4 {
		t.Fatalf("merged to %d fields, want 4: %+v", len(specs), specs)
	}
	for _, spec := range specs[2:] {
		if spec.Field == "Enabled" {
			t.Error("the plugin's Enabled was kept alongside Azir's own")
		}
	}

	columns := SheetColumns(specs)
	want := []string{"Extension", "Display name", "Enabled", "Record calls", "Encrypt the audio"}
	if strings.Join(columns, ",") != strings.Join(want, ",") {
		t.Errorf("columns = %v, want %v", columns, want)
	}

	// Written out, read back, and mapped without anybody touching a dropdown.
	now := Current{"100": {FieldName: "Reception", FieldEnabled: "yes",
		"RecordCalls": "no", "SRTPMode": "SRTPDisabled"}}
	var out strings.Builder
	w := csv.NewWriter(&out)
	_ = w.Write(columns)
	_ = w.Write(Line("100", now["100"], specs))
	w.Flush()

	parsed, err := Parse(strings.NewReader(out.String()))
	if err != nil {
		t.Fatal(err)
	}
	guessed := Suggest(parsed.Columns, specs)
	if len(guessed.Fields) != len(specs) {
		t.Errorf("Suggest mapped %d of %d published columns: %+v",
			len(guessed.Fields), len(specs), guessed.Fields)
	}
	plan, err := Build(parsed, guessed, now, specs)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Changing != 0 {
		t.Errorf("an unedited round trip proposed %d changes", plan.Changing)
	}

	// And an edit to a published field is seen as one change, in its words.
	parsed.Rows[0][3] = "yes" // Record calls
	plan, err = Build(parsed, guessed, now, specs)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Changing != 1 || len(plan.Rows[0].Changes) != 1 {
		t.Fatalf("editing an option gave %+v", plan.Rows)
	}
	if got := plan.Rows[0].Changes[0]; got.Label != "Record calls" || got.Before != "no" || got.After != "yes" {
		t.Errorf("change reads as %+v", got)
	}
}

// A choice is checked against what the phone system accepts, so a typo is a
// skipped row with a reason rather than a failure halfway through a batch.
func TestAChoiceIsCheckedBeforeItIsSent(t *testing.T) {
	spec := Spec{Field: "SRTPMode", Label: "Encrypt", Kind: KindChoice,
		Choices: []string{"SRTPDisabled", "SRTPEnabled"}}

	if got, err := Normalise(spec, "srtpenabled"); err != nil || got != "SRTPEnabled" {
		t.Errorf("Normalise(%q) = %q, %v; want the canonical spelling", "srtpenabled", got, err)
	}
	if _, err := Normalise(spec, "SRTPMaybe"); err == nil {
		t.Error("a value the phone system does not take was accepted")
	}
	if got, _ := Normalise(Spec{Kind: KindBool}, "TRUE"); got != "yes" {
		t.Errorf(`Normalise bool "TRUE" = %q, want "yes"`, got)
	}
}

/*
Choosing extensions in the console produces the same plan a sheet does.

Same comparison, same rows, same counts — which is what makes the diff, the
approval, the audit line and the undo work for both without a second path
through any of them.
*/
func TestChoosingIsTheSameComparison(t *testing.T) {
	specs := Merge(Core, []Spec{{Field: "RecordCalls", Label: "Record calls", Kind: KindBool}})
	now := Current{
		"100": {FieldName: "Reception", FieldEnabled: "yes", "RecordCalls": "no"},
		"101": {FieldName: "Sales", FieldEnabled: "yes", "RecordCalls": "yes"},
	}

	plan := Choose(map[string]Values{
		"100": {"RecordCalls": "yes"}, // differs
		"101": {"RecordCalls": "yes"}, // already so
		"999": {"RecordCalls": "yes"}, // not there
	}, now, specs)

	if plan.Changing != 1 || plan.Unchanged != 1 || plan.Skipped != 1 {
		t.Fatalf("changing=%d unchanged=%d skipped=%d; want 1/1/1",
			plan.Changing, plan.Unchanged, plan.Skipped)
	}
	// A field nobody set is left alone, not blanked.
	if got := plan.Rows[0]; len(got.Changes) != 1 || got.Changes[0].Field != "RecordCalls" {
		t.Errorf("choosing one option proposed %+v", got.Changes)
	}
}

/*
A secret never reaches a sheet, a plan or a diff.

A voicemail PIN is a credential. A plan is stored in the database, drawn on a
screen, kept in the activity log and handed back as an undo, so a value that
went through it would be a credential at rest in four places — and there is
nothing to compare it against anyway, since nothing reads it back, so every
row would report a change forever.
*/
func TestSecretsStayOutOfEverything(t *testing.T) {
	specs := Merge(Core, []Spec{
		{Field: "VMPIN", Label: "Voicemail PIN", Kind: KindSecret, Group: "Voicemail"},
		{Field: "VMEnabled", Label: "Voicemail", Kind: KindBool, Group: "Voicemail"},
	})

	for _, column := range SheetColumns(specs) {
		if strings.Contains(strings.ToLower(column), "pin") {
			t.Errorf("a sheet carries %q, so a PIN can be exported", column)
		}
	}
	if _, mapped := CanonicalMapping(specs).Fields["VMPIN"]; mapped {
		t.Error("a sheet column maps to the PIN")
	}
	if got := Suggest([]string{"Extension", "Voicemail PIN"}, specs); len(got.Fields) != 0 {
		t.Errorf("Suggest mapped a PIN column: %+v", got.Fields)
	}

	// Line writes one cell per sheeted column, so a secret must not shift the
	// row by one and quietly put the PIN under the next heading.
	line := Line("100", Values{FieldName: "Reception", "VMPIN": "1234", "VMEnabled": "yes"}, specs)
	if len(line) != len(SheetColumns(specs)) {
		t.Fatalf("Line wrote %d cells for %d columns", len(line), len(SheetColumns(specs)))
	}
	for _, cell := range line {
		if cell == "1234" {
			t.Error("the PIN was written into the sheet")
		}
	}

	// And it is never a change, however it is asked for.
	now := Current{"100": {FieldName: "Reception", "VMPIN": "", "VMEnabled": "yes"}}
	plan := Choose(map[string]Values{"100": {"VMPIN": "9999"}}, now, specs)
	if plan.Changing != 0 {
		t.Errorf("setting a PIN produced a comparable change: %+v", plan.Rows)
	}
}

/*
A destination is a field like any other.

Written as one string so a forwarding rule can sit in a sheet, in a diff and in
a bulk edit without any of them learning what a destination is. Everything
below is what somebody would actually type.
*/
func TestWhereACallGoes(t *testing.T) {
	spec := Spec{
		Field: "NoAnswerExternal", Label: "No answer", Kind: KindDestination,
		Choices: []string{"None", "VoiceMail", "Extension", "External", "Queue", "RingGroup"},
	}

	for _, c := range []struct{ in, want string }{
		{"VoiceMail", "VoiceMail"},
		{"voicemail", "VoiceMail"},
		// The phone system keeps the extension's own number beside a voicemail
		// rule. It is whose voicemail it is, not where the call goes.
		{"VoiceMail:100", "VoiceMail"},
		{"None:100", "None"},
		{"None", "None"},
		{"Extension:101", "Extension:101"},
		{"extension: 101", "Extension:101"},
		{"External:5551234", "External:5551234"},
	} {
		got, err := Normalise(spec, c.in)
		if err != nil || got != c.want {
			t.Errorf("Normalise(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}

	// A place with no address sends calls into silence, which is worse than
	// refusing the change.
	for _, bad := range []string{"Extension", "External", "Queue", "Somewhere", "Extension:"} {
		if got, err := Normalise(spec, bad); err == nil {
			t.Errorf("Normalise(%q) = %q; want a refusal", bad, got)
		}
	}

	if where, number := Where("Extension:101"); where != "Extension" || number != "101" {
		t.Errorf(`Where("Extension:101") = %q, %q`, where, number)
	}
	if where, number := Where("VoiceMail"); where != "VoiceMail" || number != "" {
		t.Errorf(`Where("VoiceMail") = %q, %q`, where, number)
	}
}

// Where a phone system offers the parts of a name, the display name stops
// being something a form should offer — two controls over one setting is the
// bug. It stays in sheets, which have always had one column for a name.
func TestTheDisplayNameLeavesTheFormWhenThePartsArrive(t *testing.T) {
	alone := Merge(Core, []Spec{{Field: "RecordCalls", Label: "Record calls", Kind: KindBool}})
	for _, spec := range alone {
		if spec.Field == FieldName && spec.SheetOnly {
			t.Error("the display name left the form on a phone system that has no other name field")
		}
	}

	split := Merge(Core, []Spec{
		{Field: FieldFirst, Label: "First name", Kind: KindText},
		{Field: FieldLast, Label: "Last name", Kind: KindText},
	})
	shown := false
	for _, spec := range split {
		if spec.Field == FieldName {
			shown = !spec.SheetOnly
		}
	}
	if shown {
		t.Error("the form offers the display name and its parts, which fight over one setting")
	}
	// And it is still a column, because a sheet has one name column.
	found := false
	for _, column := range SheetColumns(split) {
		if column == "Display name" {
			found = true
		}
	}
	if !found {
		t.Error("the display name left the spreadsheet too")
	}
}

/*
A read-only field is exported, marked, and never written back.

The three halves of one property, asserted together because they only mean
anything together. Carrying the DID assigned to an extension in the sheet is
worth having beside the outbound caller ID somebody is about to edit; letting
that column be typed into and applied would mean the extension sheet quietly
changing inbound routing.
*/
func TestAReadOnlyFieldIsExportedAndNeverWrittenBack(t *testing.T) {
	specs := []Spec{
		{Field: FieldName, Label: "Name", Kind: KindText},
		{Field: "OutboundCallerID", Label: "Outbound caller ID", Kind: KindText},
		{Field: "AssignedDIDs", Label: "Assigned DID", Kind: KindReadOnly},
	}

	// Exported, so it is in the file somebody downloads.
	columns := SheetColumns(specs)
	found := ""
	for _, name := range columns {
		if strings.HasPrefix(name, "Assigned DID") {
			found = name
		}
	}
	if found == "" {
		t.Fatalf("the read-only column is not in the sheet: %v", columns)
	}

	// Marked, so nobody fills it in expecting it to apply.
	if !strings.Contains(found, "read-only") {
		t.Errorf("the column reads %q and does not say it is read-only", found)
	}

	// And refused on the way back in, whatever the header says.
	for _, header := range []string{found, "Assigned DID", "AssignedDIDs"} {
		got := Suggest([]string{"Extension", header}, specs)
		if _, mapped := got.Fields["AssignedDIDs"]; mapped {
			t.Errorf("a column headed %q was mapped to a read-only field", header)
		}
	}
}

// The columns, the mapping and the row have to stay a column apart from each
// other, and a read-only field is the thing most likely to knock them out of
// step because only some of the three used to skip it.
func TestAReadOnlyFieldKeepsTheColumnsAligned(t *testing.T) {
	specs := []Spec{
		{Field: FieldName, Label: "Name", Kind: KindText},
		{Field: "AssignedDIDs", Label: "Assigned DID", Kind: KindReadOnly},
		{Field: "OutboundCallerID", Label: "Outbound caller ID", Kind: KindText},
		{Field: "VMPIN", Label: "Voicemail PIN", Kind: KindSecret},
	}

	columns := SheetColumns(specs)
	line := Line("101", Values{
		FieldName:          "Reception",
		"AssignedDIDs":     "+15551110000 +15551110001",
		"OutboundCallerID": "+15551110000",
		"VMPIN":            "1234",
	}, specs)

	if len(line) != len(columns) {
		t.Fatalf("Line wrote %d cells for %d columns: %v against %v", len(line), len(columns), line, columns)
	}
	// The secret is in neither.
	for _, cell := range line {
		if cell == "1234" {
			t.Fatal("a voicemail PIN was written into a downloaded sheet")
		}
	}
	// And the mapping points at the same column the value was written to.
	mapping := CanonicalMapping(specs)
	if got := line[mapping.Fields["OutboundCallerID"]]; got != "+15551110000" {
		t.Errorf("the mapping points at %q, which is not the caller ID", got)
	}
	if got := line[mapping.Fields["AssignedDIDs"]]; got != "+15551110000 +15551110001" {
		t.Errorf("the mapping points at %q, which is not the assigned DIDs", got)
	}
}
