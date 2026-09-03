package bulk

import (
	"strings"
	"testing"
)

// A bare column of numbers with no header is the file this was written for,
// and the first number is the one a header row would eat. It has to survive.
func TestParseDIDFileKeepsAHeaderlessFirstNumber(t *testing.T) {
	got, err := ParseDIDFile(strings.NewReader("+15551234500\n+15551234501\n+15551234502\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"+15551234500", "+15551234501", "+15551234502"}
	assertNumbers(t, got, want)
}

// The same file with one number in it parses as a header and no rows, which
// Parse calls an error. Here it is a one-number import.
func TestParseDIDFileReadsASingleNumber(t *testing.T) {
	got, err := ParseDIDFile(strings.NewReader("5551234500\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	assertNumbers(t, got, []string{"5551234500"})
}

// A real header is a header, and is not imported as a number.
func TestParseDIDFileSkipsARealHeader(t *testing.T) {
	got, err := ParseDIDFile(strings.NewReader("DID Number\n+15551234500\n+15551234501\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	assertNumbers(t, got, []string{"+15551234500", "+15551234501"})
}

// Formatting comes off. The number underneath is not otherwise touched — no
// country code is added, and one that is there is not removed.
func TestParseDIDsStripsFormattingAndNothingElse(t *testing.T) {
	sheet := Sheet{Columns: []string{"did"}, Rows: [][]string{
		{"(555) 123-4500"},
		{"+1 555 123 4501"},
		{"555.123.4502"},
		{"5551234503"},
	}}
	assertNumbers(t, ParseDIDs(sheet), []string{
		"5551234500", "+15551234501", "5551234502", "5551234503",
	})
}

/*
The number a carrier sends is the number the trunk has to match, so a DID
without a country code must stay without one.

Written as its own test because "helpfully" normalising to E.164 is the
obvious-looking improvement somebody will reach for, and it would produce a
trunk full of numbers that look right and never ring.
*/
func TestParseDIDsNeverAddsACountryCode(t *testing.T) {
	sheet := Sheet{Columns: []string{"did"}, Rows: [][]string{{"5551234500"}}}
	got := ParseDIDs(sheet)
	assertNumbers(t, got, []string{"5551234500"})
	if strings.HasPrefix(got.Numbers[0].Number, "+") {
		t.Fatal("a plus was added to a number that did not have one")
	}
}

func TestParseDIDsExpandsARange(t *testing.T) {
	sheet := Sheet{Columns: []string{"did"}, Rows: [][]string{{"+15551234510..514"}}}
	assertNumbers(t, ParseDIDs(sheet), []string{
		"+15551234510", "+15551234511", "+15551234512", "+15551234513", "+15551234514",
	})
}

// A range whose end wraps a digit width keeps the width it was written at, so
// nothing comes out a digit short.
func TestParseDIDsRangeKeepsItsWidth(t *testing.T) {
	sheet := Sheet{Columns: []string{"did"}, Rows: [][]string{{"+15551234097..101"}}}
	assertNumbers(t, ParseDIDs(sheet), []string{
		"+15551234097", "+15551234098", "+15551234099", "+15551234100", "+15551234101",
	})
}

// A dash is formatting, never a range. Reading it as a range would put twenty
// numbers nobody asked for onto a trunk.
func TestParseDIDsTreatsADashAsFormatting(t *testing.T) {
	sheet := Sheet{Columns: []string{"did"}, Rows: [][]string{{"555-1234"}}}
	assertNumbers(t, ParseDIDs(sheet), []string{"5551234"})
}

func TestParseDIDsRefusesWhatIsNotANumber(t *testing.T) {
	sheet := Sheet{Columns: []string{"did"}, Rows: [][]string{
		{"+15551234500"},
		{"not a number"},
		{"+15551234501"},
	}}
	got := ParseDIDs(sheet)
	assertNumbers(t, got, []string{"+15551234500", "+15551234501"})
	if len(got.Refused) != 1 {
		t.Fatalf("refused %d cells, want 1: %+v", len(got.Refused), got.Refused)
	}
	if got.Refused[0].Row != 3 {
		t.Errorf("refusal points at line %d, want 3", got.Refused[0].Row)
	}
	if !strings.Contains(got.Refused[0].Why, "not a phone number") {
		t.Errorf("refusal reads %q", got.Refused[0].Why)
	}
}

// A refused cell must not take the rest of the file down with it: a carrier
// export with one bad line still has hundreds of good ones.
func TestParseDIDsRefusalDoesNotStopTheImport(t *testing.T) {
	sheet := Sheet{Columns: []string{"did"}, Rows: [][]string{
		{"nonsense"}, {"+15551234500"}, {"also nonsense"}, {"+15551234501"},
	}}
	got := ParseDIDs(sheet)
	assertNumbers(t, got, []string{"+15551234500", "+15551234501"})
	if len(got.Refused) != 2 {
		t.Fatalf("refused %d cells, want 2", len(got.Refused))
	}
}

func TestParseDIDsCountsRepeatsRatherThanRefusingThem(t *testing.T) {
	sheet := Sheet{Columns: []string{"did"}, Rows: [][]string{
		{"+15551234500"}, {"+1 (555) 123 4500"}, {"+1-555-123-4500"}, {"+15551234501"},
	}}
	got := ParseDIDs(sheet)
	// The second and third differ from the first only in formatting, so they
	// are the same number written three ways.
	assertNumbers(t, got, []string{"+15551234500", "+15551234501"})
	if got.Repeated != 2 {
		t.Errorf("counted %d repeats, want 2", got.Repeated)
	}
	if len(got.Refused) != 0 {
		t.Errorf("a repeat was refused: %+v", got.Refused)
	}
}

func TestParseDIDsFindsTheNamedColumn(t *testing.T) {
	sheet := Sheet{Columns: []string{"Description", "DID Number", "Notes"}, Rows: [][]string{
		{"Main line", "+15551234500", "reception"},
		{"Fax", "+15551234501", ""},
	}}
	assertNumbers(t, ParseDIDs(sheet), []string{"+15551234500", "+15551234501"})
}

func TestExpandDIDRefusesABackwardsRange(t *testing.T) {
	if _, err := expandDID("+15551234510..505"); err == nil {
		t.Fatal("a range ending before it starts was accepted")
	}
}

func TestExpandDIDRefusesARangeLongerThanItsNumber(t *testing.T) {
	if _, err := expandDID("555..12345678"); err == nil {
		t.Fatal("a range end longer than its number was accepted")
	}
}

func TestExpandDIDRefusesAnEnormousRange(t *testing.T) {
	if _, err := expandDID("+1555000000..999999"); err == nil {
		t.Fatal("a range of a million numbers was accepted")
	}
}

func assertNumbers(t *testing.T, got DIDs, want []string) {
	t.Helper()
	if len(got.Numbers) != len(want) {
		t.Fatalf("got %d numbers, want %d: %+v", len(got.Numbers), len(want), got.Numbers)
	}
	for i, number := range want {
		if got.Numbers[i].Number != number {
			t.Errorf("number %d is %q, want %q", i, got.Numbers[i].Number, number)
		}
	}
}

// The two-column file the import is actually for: a DID and the extension it
// belongs to, with no header on either.
func TestParseDIDFileReadsAHeaderlessDestinationColumn(t *testing.T) {
	got, err := ParseDIDFile(strings.NewReader("+15551234500,101\n+15551234501,102\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	assertNumbers(t, got, []string{"+15551234500", "+15551234501"})
	assertExtensions(t, got, []string{"101", "102"})
}

// The second column is a number and nothing else. What that number turns out
// to be — a person, a queue, a ring group — is resolved against the phone
// system at import, because that is the only place that knows.
func TestParseDIDsReadsTheExtensionAsAPlainNumber(t *testing.T) {
	sheet := Sheet{Columns: []string{"did", "extension"}, Rows: [][]string{
		{"+15551234500", "101"},
		{"+15551234501", "800"},
	}}
	assertExtensions(t, ParseDIDs(sheet), []string{"101", "800"})
}

/*
Naming a type is refused rather than half-understood.

3CX numbering is one plan, so "Queue:800" says nothing the number does not
already say. Accepting it would mean carrying a vocabulary this no longer uses,
and quietly ignoring a prefix somebody wrote on purpose is worse than telling
them it is not needed.
*/
func TestParseDIDsRefusesATypePrefix(t *testing.T) {
	sheet := Sheet{Columns: []string{"did", "extension"}, Rows: [][]string{
		{"+15551234500", "Queue:800"},
	}}
	got := ParseDIDs(sheet)
	if len(got.Refused) != 1 {
		t.Fatalf("refused %d rows, want 1: %+v", len(got.Refused), got.Refused)
	}
	if !strings.Contains(got.Refused[0].Why, "just the number") {
		t.Errorf("refusal reads %q, and does not say what to write instead", got.Refused[0].Why)
	}
}

// A number with no destination is imported unrouted rather than refused. That
// is the single-column file, which is a real and useful import.
func TestParseDIDsAllowsAMissingDestination(t *testing.T) {
	sheet := Sheet{Columns: []string{"did", "extension"}, Rows: [][]string{
		{"+15551234500", "101"},
		{"+15551234501", ""},
	}}
	got := ParseDIDs(sheet)
	assertNumbers(t, got, []string{"+15551234500", "+15551234501"})
	assertExtensions(t, got, []string{"101", ""})
	if len(got.Refused) != 0 {
		t.Fatalf("a number with no destination was refused: %+v", got.Refused)
	}
}

/*
A destination that cannot be read refuses the whole row.

Importing the number and dropping the routing would leave a live DID pointing
wherever the trunk's default sends it, with nothing on the screen to say the
second column had been ignored.
*/
func TestParseDIDsRefusesTheRowWhenTheDestinationIsWrong(t *testing.T) {
	sheet := Sheet{Columns: []string{"did", "extension"}, Rows: [][]string{
		{"+15551234500", "reception desk"},
		{"+15551234501", "101"},
	}}
	got := ParseDIDs(sheet)
	assertNumbers(t, got, []string{"+15551234501"})
	if len(got.Refused) != 1 {
		t.Fatalf("refused %d rows, want 1: %+v", len(got.Refused), got.Refused)
	}
	if !strings.Contains(got.Refused[0].Why, "not an extension number") {
		t.Errorf("refusal reads %q", got.Refused[0].Why)
	}
}

// A named destination column is found wherever it sits, and a column that is
// not one is left alone — a Notes column beside the numbers is not routing.
func TestParseDIDsIgnoresAnUnnamedColumnWhenTheSheetHasHeaders(t *testing.T) {
	sheet := Sheet{Columns: []string{"DID", "Notes"}, Rows: [][]string{
		{"+15551234500", "main reception line"},
	}}
	got := ParseDIDs(sheet)
	assertNumbers(t, got, []string{"+15551234500"})
	assertExtensions(t, got, []string{""})
	if len(got.Refused) != 0 {
		t.Fatalf("a notes column was read as routing: %+v", got.Refused)
	}
}

// A range with a destination sends the whole block to one place, which is what
// "these twenty all ring reception" means.
func TestParseDIDsGivesARangeOneDestination(t *testing.T) {
	sheet := Sheet{Columns: []string{"did", "extension"}, Rows: [][]string{
		{"+15551234510..512", "101"},
	}}
	got := ParseDIDs(sheet)
	assertNumbers(t, got, []string{"+15551234510", "+15551234511", "+15551234512"})
	assertExtensions(t, got, []string{"101", "101", "101"})
}

func assertExtensions(t *testing.T, got DIDs, want []string) {
	t.Helper()
	if len(got.Numbers) != len(want) {
		t.Fatalf("got %d numbers, want %d: %+v", len(got.Numbers), len(want), got.Numbers)
	}
	for i, to := range want {
		if got.Numbers[i].Extension != to {
			t.Errorf("number %d rings %q, want %q", i, got.Numbers[i].Extension, to)
		}
	}
}
