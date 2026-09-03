package bulk

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

/*
Reading a list of DID numbers out of a sheet.

Separate from the extension flow next door and deliberately much smaller. A
sheet of extensions is a set of fields compared field by field; a sheet of DIDs
is a set of numbers, and what a person approves is which of them the trunk does
not have yet. There is no mapping step because there is nothing to map.

The one thing this shares with the extension flow is Parse, and that is the
part worth sharing: delimiter sniffing, the byte-order mark Excel writes, the
size and row limits. Everything below is about what a phone number is.
*/

// Limits on one import. MaxRows already bounds the sheet; ranges are what can
// turn a small sheet into a large import, so the count of numbers is bounded
// separately from the count of rows.
const MaxDIDs = 5000

// RangeOperator separates the two ends of a block of consecutive numbers.
//
// Two dots rather than a dash, because a dash is how half the world writes a
// phone number and there is no way to tell "555-1234" the formatting from
// "510-529" the range without guessing. Guessing wrong here writes twenty
// numbers nobody asked for onto a customer's trunk.
const RangeOperator = ".."

// DID is one number as the sheet gave it and as it will be written.
type DID struct {
	// Number is what goes to the phone system: the raw cell with formatting
	// removed, and nothing else changed.
	Number string `json:"number"`
	/*
		Extension is the number calls for this DID should go to, exactly as the
		sheet wrote it. Empty when the sheet only gave a DID.

		A number and nothing else — not "Extension:101" or "Queue:800". A 3CX
		numbering plan is one space: a user, a queue, a ring group and a digital
		receptionist cannot share a number, so the number already says which one
		it is and asking somebody to say it again is asking them to get it
		wrong. What kind of thing it is gets resolved against the phone system
		at import, where the answer is a fact rather than a guess.
	*/
	Extension string `json:"extension,omitempty"`
	// Raw is the cell it came from, kept so a refusal can quote it back.
	Raw string `json:"raw"`
	// Row is the 1-based line in the sheet, for the same reason.
	Row int `json:"row"`
}

// DIDProblem is a cell that will not be imported, and why. Written for the
// person who chose the file rather than for a log.
type DIDProblem struct {
	Raw string `json:"raw"`
	Row int    `json:"row"`
	Why string `json:"why"`
}

// DIDs is what a sheet turned into: the numbers to consider, and the cells
// that were refused.
type DIDs struct {
	Numbers  []DID        `json:"numbers"`
	Refused  []DIDProblem `json:"refused"`
	Repeated int          `json:"repeated"`
}

/*
ParseDIDFile reads a file of DID numbers.

A header-only sheet is not an error here, for the reason ErrHeaderOnly gives:
the file this exists for is a bare column of numbers, and one with a single
number in it parses as a header and nothing else. Every other refusal Parse
makes is still a refusal.
*/
func ParseDIDFile(r io.Reader) (DIDs, error) {
	sheet, err := Parse(r)
	if err != nil && !errors.Is(err, ErrHeaderOnly) {
		return DIDs{}, err
	}
	return ParseDIDs(sheet), nil
}

/*
ParseDIDs reads the numbers out of a parsed sheet.

Only one column is read. Which one is worked out rather than asked for: a
header naming a DID if there is one, and otherwise the first column, because
the file this is written for is a single column of numbers exported from a
carrier portal.

That file usually has no header at all, which is the trap this has to handle.
Parse takes the first line as column names, so a headerless list of DIDs would
silently lose its first number — the one case where being wrong costs a working
phone number rather than an error message. So the header is checked: if it
reads as a phone number, it was data, and it is put back.
*/
func ParseDIDs(sheet Sheet) DIDs {
	column := didColumn(sheet.Columns)
	// Whether the first line was data decides how the second column is found,
	// so it has to be settled before looking for one.
	headless := looksLikeData(sheet.Columns, column)
	toColumn := extensionColumn(sheet.Columns, column, headless)

	var out DIDs
	seen := map[string]bool{}

	// One row of the sheet, reduced to the two cells that matter.
	type line struct {
		did string
		to  string
		at  int
	}

	/*
		Line numbers as the person sees them in a text editor.

		The first line of the file is line 1 whether it turned out to be column
		names or the first number, so everything Parse handed back as a row
		starts at line 2 either way.
	*/
	rows := make([]line, 0, len(sheet.Rows)+1)
	if headless {
		rows = append(rows, line{
			did: cell(sheet.Columns, column),
			to:  cell(sheet.Columns, toColumn),
			at:  1,
		})
	}
	for i, row := range sheet.Rows {
		rows = append(rows, line{did: cell(row, column), to: cell(row, toColumn), at: i + 2})
	}

	for _, row := range rows {
		raw := strings.TrimSpace(row.did)
		if raw == "" {
			continue
		}
		numbers, err := expandDID(raw)
		if err != nil {
			out.Refused = append(out.Refused, DIDProblem{Raw: raw, Row: row.at, Why: err.Error()})
			continue
		}

		to, err := extensionOf(row.to)
		if err != nil {
			// The DID is refused too rather than imported unassigned. A row
			// that named an extension meant it, and importing the number while
			// dropping the part that was mistyped is how somebody ends up with
			// a live DID ringing nowhere and no error to explain it.
			out.Refused = append(out.Refused, DIDProblem{Raw: raw, Row: row.at, Why: err.Error()})
			continue
		}

		for _, number := range numbers {
			if seen[number] {
				// Not a refusal. A carrier export listing a number twice is
				// not a mistake anybody needs to fix before importing; it
				// just means the number is imported once.
				out.Repeated++
				continue
			}
			seen[number] = true
			if len(out.Numbers) >= MaxDIDs {
				out.Refused = append(out.Refused, DIDProblem{
					Raw: raw, Row: row.at,
					Why: fmt.Sprintf("this import is already at its limit of %d numbers", MaxDIDs),
				})
				return out
			}
			out.Numbers = append(out.Numbers, DID{
				Number: number, Extension: to, Raw: raw, Row: row.at,
			})
		}
	}
	return out
}

// cell reads one column out of a row, treating a short row as empty rather
// than as an error. Sheets exported from real spreadsheets are ragged.
func cell(row []string, column int) string {
	if column < 0 || column >= len(row) {
		return ""
	}
	return row[column]
}

/*
extensionOf reads the second column: the number this DID should ring.

A number and nothing else. Whether it turns out to be a person's extension, a
queue, a ring group or a digital receptionist is not asked here and is not the
sheet's business — 3CX gives all of them numbers out of one plan, so the number
identifies the thing on its own. The import resolves what it is against the
phone system, which is the only place that actually knows.

Formatting comes off for the same reason it does on a DID: somebody pasting
from a spreadsheet gets spaces and the occasional stray dash.
*/
func extensionOf(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '\t':
			// Formatting. Dropped.
		default:
			return "", fmt.Errorf("%q is not an extension number — write just the number, as 101", raw)
		}
	}

	number := b.String()
	if number == "" {
		return "", fmt.Errorf("%q is not an extension number — write just the number, as 101", raw)
	}
	// No upper bound worth enforcing. 3CX numbering plans run to five digits on
	// a large site and there is nothing to gain by guessing at a ceiling here;
	// a number that does not exist is refused by the phone system, by name.
	if len(number) > 10 {
		return "", fmt.Errorf("%q is too long to be an extension number", raw)
	}
	return number, nil
}

/*
extensionColumn finds the column saying which extension each DID rings.

By name when the sheet has a header, and by position only when it has none —
the file this is written for is two bare columns, the DID and the extension it
belongs to.

The position is not a fallback for a sheet that does have headers, which is the
distinction worth keeping. A carrier export with a Notes column beside the
numbers would otherwise have its notes read as extension numbers, and every row
of it refused for naming something that is not one.
*/
func extensionColumn(columns []string, did int, headless bool) int {
	for i, name := range columns {
		if i == did {
			continue
		}
		switch squash(name) {
		case "extension", "ext", "destination", "dest", "routeto", "goesto",
			"to", "target", "extensionnumber":
			return i
		}
	}
	if headless && did+1 < len(columns) {
		return did + 1
	}
	return -1
}

// didColumn picks the column holding the numbers: one whose name says so, or
// the first. Names are matched loosely because carrier exports have opinions —
// "DID", "DID Number", "Number", "Phone Number", "TN" are all the same column.
func didColumn(columns []string) int {
	for i, name := range columns {
		switch squash(name) {
		case "did", "didnumber", "didnumbers", "number", "numbers",
			"phonenumber", "phone", "tn", "e164", "msisdn":
			return i
		}
	}
	return 0
}

/*
looksLikeData reports that the sheet has no header row at all.

A first cell that reads as a phone number was never a column name. Nothing else
is treated this way: a header of "DID" or "Number" or anything else with a
letter in it is a header, and only the file with no header is put back
together.
*/
func looksLikeData(columns []string, column int) bool {
	if column < 0 || column >= len(columns) {
		return false
	}
	first := strings.TrimSpace(columns[column])
	if first == "" {
		return false
	}
	_, err := expandDID(first)
	return err == nil
}

/*
expandDID turns one cell into the numbers it names.

A cell is either one number or a block of consecutive ones written
"+15551234510..529", where the right-hand side replaces as many digits off the
end of the left. That shorthand is what makes a block of a hundred numbers one
line of a sheet rather than a hundred.
*/
func expandDID(cell string) ([]string, error) {
	from, to, isRange := strings.Cut(cell, RangeOperator)
	if !isRange {
		number, err := cleanDID(cell)
		if err != nil {
			return nil, err
		}
		return []string{number}, nil
	}

	first, err := cleanDID(from)
	if err != nil {
		return nil, err
	}
	last := strings.TrimSpace(to)
	// The right-hand side is a plain tail of digits — no plus, no formatting.
	// Anything else is somebody writing a range they mean differently from the
	// way this reads it, and the answer to that is to say so rather than to
	// import a guess.
	if last == "" || !allDigits(last) {
		return nil, fmt.Errorf("the end of that range should be the last digits of the number, as in 5551234510%s529", RangeOperator)
	}

	digits := strings.TrimPrefix(first, "+")
	if len(last) > len(digits) {
		return nil, fmt.Errorf("the end of that range is longer than the number it belongs to")
	}
	prefix := first[:len(first)-len(last)]

	start, err := strconv.Atoi(digits[len(digits)-len(last):])
	if err != nil {
		return nil, fmt.Errorf("that range could not be read")
	}
	end, err := strconv.Atoi(last)
	if err != nil {
		return nil, fmt.Errorf("that range could not be read")
	}
	if end < start {
		return nil, fmt.Errorf("that range ends before it starts")
	}
	if end-start+1 > MaxDIDs {
		return nil, fmt.Errorf("that range is more than %d numbers", MaxDIDs)
	}

	out := make([]string, 0, end-start+1)
	for n := start; n <= end; n++ {
		// Padded back to the width it was written at, so a range ending 099
		// does not produce a number one digit short.
		out = append(out, prefix+fmt.Sprintf("%0*d", len(last), n))
	}
	return out, nil
}

/*
cleanDID strips formatting off a number and refuses anything else.

Formatting only — spaces, dashes, brackets, dots. The country code is never
added and never taken away, and a number without a leading plus does not get
one. That restraint is the whole point: a DID has to match what the carrier
actually puts in the SIP request, and a system that helpfully turned
5551234500 into +15551234500 would produce a trunk full of numbers that look
right and never ring.
*/
func cleanDID(cell string) (string, error) {
	raw := strings.TrimSpace(cell)
	if raw == "" {
		return "", fmt.Errorf("that cell is empty")
	}

	var b strings.Builder
	for i, r := range raw {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.' || r == '/' || r == '\t':
			// Formatting. Dropped.
		default:
			return "", fmt.Errorf("%q is not a phone number", raw)
		}
	}

	number := b.String()
	digits := strings.TrimPrefix(number, "+")
	if digits == "" {
		return "", fmt.Errorf("%q is not a phone number", raw)
	}
	// E.164 allows fifteen digits. The floor is deliberately low: plenty of
	// carriers deliver a DID as the last four digits and the trunk has to
	// match what arrives, not what a standard says should.
	if len(digits) < 2 || len(digits) > 18 {
		return "", fmt.Errorf("%q is the wrong length for a phone number", raw)
	}
	return number, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// squash lowercases a column name and drops everything that is not a letter or
// a digit, so "DID Number", "did_number" and "DIDNumber" are one name.
func squash(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
