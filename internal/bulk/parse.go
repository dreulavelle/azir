// Package bulk turns an uploaded sheet into a reviewed set of changes.
//
// The file never reaches the model. It is parsed here, compared against what
// the connected system currently says, and shown to a person as a
// before-and-after they approve or reject. That is the whole point of the
// package existing rather than the assistant being handed a spreadsheet: a
// customer's extension list is their data, and the rule everywhere else in
// Azir is that it does not leave for a model to read.
package bulk

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Limits on what will be read. A sheet larger than this is a mistake or a
// different problem than the one this solves.
const (
	MaxBytes = 4 << 20 // 4 MiB
	MaxRows  = 2000
)

// Sheet is a parsed file: a header row and the rows under it.
type Sheet struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

/*
MarshalJSON writes the lists as empty rather than as null.

A sheet with no rows is a real thing: the undo of a sheet that only created
extensions has nothing to put back, only things to remove. Encoded as null it
reached the console as a value nothing could iterate, and the list of recent
sheets crashed on a row already in the database. Fixed where the value is
written rather than at each of the places it is read.
*/
func (s Sheet) MarshalJSON() ([]byte, error) {
	// A local type so marshalling does not recurse into this method.
	type plain Sheet
	if s.Rows == nil {
		s.Rows = [][]string{}
	}
	if s.Columns == nil {
		s.Columns = []string{}
	}
	return json.Marshal(plain(s))
}

// Cell returns one value by column index, or empty when the row is short.
// Sheets exported from real spreadsheets have ragged rows constantly.
func (s Sheet) Cell(row int, col int) string {
	if row < 0 || row >= len(s.Rows) || col < 0 || col >= len(s.Rows[row]) {
		return ""
	}
	return strings.TrimSpace(s.Rows[row][col])
}

/*
Parse reads a delimited sheet.

The delimiter is worked out rather than asked for. Somebody exporting from
Excel gets a comma, from a European Excel a semicolon, and from anything
database-shaped a tab — and being asked which one they have is a question
about their tooling, not about their phone system.

A leading byte-order mark is dropped. Excel writes one on every UTF-8 CSV it
exports, and left in place it becomes part of the first column's name, so the
header reads as "Extension" everywhere except the one place it is compared.
*/
func Parse(r io.Reader) (Sheet, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return Sheet{}, fmt.Errorf("bulk: read: %w", err)
	}
	if len(raw) > MaxBytes {
		return Sheet{}, fmt.Errorf("that file is larger than %d MB", MaxBytes>>20)
	}
	text := strings.TrimPrefix(string(raw), "\ufeff")
	if strings.TrimSpace(text) == "" {
		return Sheet{}, errors.New("that file is empty")
	}
	if !utf8.ValidString(text) {
		return Sheet{}, errors.New("that file is not text — save it as CSV and try again")
	}

	reader := csv.NewReader(strings.NewReader(text))
	reader.Comma = delimiter(text)
	// Ragged rows are the norm in exported sheets, so they are read rather
	// than refused; Cell treats a missing column as empty.
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return Sheet{}, fmt.Errorf("that file could not be read as a sheet: %w", err)
	}
	if len(records) == 0 {
		return Sheet{}, errors.New("that file is empty")
	}

	head := records[0]
	columns := make([]string, len(head))
	for i, name := range head {
		columns[i] = strings.TrimSpace(name)
	}

	rows := make([][]string, 0, len(records)-1)
	for _, record := range records[1:] {
		if blank(record) {
			continue
		}
		rows = append(rows, record)
		if len(rows) > MaxRows {
			return Sheet{}, fmt.Errorf("that sheet has more than %d rows", MaxRows)
		}
	}
	if len(rows) == 0 {
		// The sheet comes back alongside the error rather than being thrown
		// away, because for one caller this is not an error at all: a list of
		// DIDs with no header and one number in it is a header and no rows,
		// and the number is in the part that would otherwise be discarded.
		return Sheet{Columns: columns, Rows: [][]string{}}, ErrHeaderOnly
	}
	return Sheet{Columns: columns, Rows: rows}, nil
}

// ErrHeaderOnly is a sheet with column names and nothing under them. Its own
// error because whether that is a mistake depends on what was expected: a
// sheet of extensions with no rows is empty, and a single column of DIDs with
// no header is a file with exactly one number in it.
var ErrHeaderOnly = errors.New("that sheet has a header and no rows")

// delimiter guesses which character separates the columns, by counting them in
// the header line. Whichever appears most is the one.
func delimiter(text string) rune {
	line, _, _ := strings.Cut(text, "\n")
	best, most := ',', 0
	for _, candidate := range []rune{',', ';', '\t', '|'} {
		if n := strings.Count(line, string(candidate)); n > most {
			best, most = candidate, n
		}
	}
	return best
}

func blank(record []string) bool {
	for _, cell := range record {
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
}
