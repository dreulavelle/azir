package bulk

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

/*
Turning a sheet into a before-and-after.

The rule this package exists to keep is that nothing is applied from what the
file said alone. Every row is matched against what the phone system reports
right now, and what a person approves is the difference between those two —
not the sheet, and not a summary of it.

That matters most where it is least visible. A sheet exported last Tuesday and
edited since describes a system that has moved on; applying it wholesale would
quietly undo whatever changed in between. Comparing first means those rows show
up as changes a person can see and decline, rather than as work nobody asked
for.
*/

// Field is something a sheet can set.
type Field string

const (
	FieldName    Field = "name"
	FieldEnabled Field = "enabled"
)

// Editable is every field this can change, with the words a person reads.
var Editable = []struct {
	Field Field
	Label string
}{
	{FieldName, "Display name"},
	{FieldEnabled, "Enabled"},
}

// Mapping says which sheet column carries which thing. The key column is the
// extension number; without it a row cannot be matched to anything.
type Mapping struct {
	Extension int           `json:"extension"`
	Fields    map[Field]int `json:"fields"`
	// Create turns rows the phone system does not have into new extensions
	// rather than skipping them.
	//
	// Off unless asked for, and that default is the whole safety of this
	// feature. A sheet uploaded against the wrong customer matches nothing —
	// with this off that is twenty harmless "no such extension" rows, and with
	// it on it is twenty extensions on somebody else's phone system. Turning
	// it on is a decision about what the sheet means, made while setting up
	// the comparison, not another dialog in front of the same button.
	Create bool `json:"create"`
}

// Current is what the phone system says today, keyed by extension number.
type Current map[string]Values

// Values is the editable state of one extension.
type Values struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// Change is one field on one extension, before and after.
type Change struct {
	Field  Field  `json:"field"`
	Label  string `json:"label"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Row is one extension's worth of the plan.
type Row struct {
	Extension string   `json:"extension"`
	Name      string   `json:"name"`
	Changes   []Change `json:"changes,omitempty"`
	// New marks a row that does not exist yet and would be created.
	New bool `json:"new,omitempty"`
	// Wanted is what a new extension would be called.
	Wanted string `json:"wanted,omitempty"`
	// Problem explains why a row will be skipped: no such extension, an
	// unreadable value, a duplicate. Rows with one are never applied.
	Problem string `json:"problem,omitempty"`
}

// Changed reports whether this row would alter an extension that exists.
func (r Row) Changed() bool { return r.Problem == "" && !r.New && len(r.Changes) > 0 }

// Creates reports whether this row would make a new extension.
func (r Row) Creates() bool { return r.Problem == "" && r.New }

// Plan is the whole before-and-after, as reviewed and as applied.
type Plan struct {
	Rows []Row `json:"rows"`
	// Counts, so the screen and the confirmation agree without recounting.
	Changing  int `json:"changing"`
	Creating  int `json:"creating"`
	Unchanged int `json:"unchanged"`
	Skipped   int `json:"skipped"`
}

/*
Build compares a mapped sheet against what the system currently reports.

Rows the system has never heard of are kept and marked rather than dropped.
A sheet naming extensions that do not exist is worth seeing — it usually means
the wrong customer, or a column shifted by one — and silently applying the rows
that happened to match would be the worst of both.
*/
func Build(sheet Sheet, mapping Mapping, now Current) (Plan, error) {
	if mapping.Extension < 0 || mapping.Extension >= len(sheet.Columns) {
		return Plan{}, errors.New("say which column holds the extension number")
	}
	if len(mapping.Fields) == 0 {
		return Plan{}, errors.New("say which column holds something to change")
	}
	for field, col := range mapping.Fields {
		if col < 0 || col >= len(sheet.Columns) {
			return Plan{}, fmt.Errorf("there is no column %d for %s", col, field)
		}
	}

	label := map[Field]string{}
	for _, e := range Editable {
		label[e.Field] = e.Label
	}

	// Where a new extension's number comes from when the sheet does not give
	// one: after the highest that exists, never filling a gap. A gap is
	// usually an extension somebody deleted, and its number can still carry
	// the DID routing and voicemail that went with it — so reusing it means a
	// caller dialling the old number reaches a new person.
	next := highest(now)

	var plan Plan
	seen := map[string]int{}

	for i := range sheet.Rows {
		extension := sheet.Cell(i, mapping.Extension)
		row := Row{Extension: extension}

		switch {
		case extension == "" && !mapping.Create:
			row.Problem = "no extension number in this row"
		case extension != "" && seen[extension] > 0:
			// Two rows for one extension is a sheet somebody edited by hand,
			// and guessing which of them was meant is not this package's
			// business.
			//
			// Only for rows that name a number. Several rows with the column
			// left blank are not duplicates of each other — they are several
			// new extensions, and each gets its own number below.
			row.Problem = fmt.Sprintf("this extension is also on row %d", seen[extension])
		case extension != "":
			seen[extension] = i + 1
		}

		state, known := now[extension]

		switch {
		case row.Problem != "":
			// Already ruled out above.
		case known:
			row.Name = state.Name
			changes, err := compare(sheet, i, mapping, state, label)
			if err != nil {
				row.Problem = err.Error()
			} else {
				row.Changes = changes
			}
		case !mapping.Create:
			row.Problem = "the phone system has no such extension"
		default:
			// A number the sheet asked for and nothing is using is the number
			// it gets; a row with no number at all gets the next one.
			row.New = true
			if extension == "" {
				next++
				row.Extension = fmt.Sprint(next)
			}
			row.Wanted = wantedName(sheet, i, mapping)
			if row.Wanted == "" {
				row.Problem = "a new extension needs a name"
				row.New = false
			}
		}

		switch {
		case row.Problem != "":
			plan.Skipped++
		case row.New:
			plan.Creating++
		case len(row.Changes) > 0:
			plan.Changing++
		default:
			plan.Unchanged++
		}
		plan.Rows = append(plan.Rows, row)
	}

	sort.SliceStable(plan.Rows, func(a, b int) bool {
		return order(plan.Rows[a]) < order(plan.Rows[b])
	})
	return plan, nil
}

// order puts the rows that do something first, then the problems, then the
// rows that change nothing — which is the order somebody reviews them in.
// New extensions sit together after the edits: they are the part of a plan
// most worth reading twice, and scattered among unchanged rows they are not
// read at all.
func order(r Row) int {
	switch {
	case r.Changed():
		return 0
	case r.Creates():
		return 1
	case r.Problem != "":
		return 2
	default:
		return 3
	}
}

// highest is the largest numeric extension the system has, so a new one can
// start after it.
func highest(now Current) int {
	top := 0
	for extension := range now {
		if n, err := strconv.Atoi(extension); err == nil && n > top {
			top = n
		}
	}
	return top
}

// wantedName reads what a new extension should be called.
func wantedName(sheet Sheet, i int, mapping Mapping) string {
	col, ok := mapping.Fields[FieldName]
	if !ok {
		return ""
	}
	return sheet.Cell(i, col)
}

// compare works out what one row would change.
func compare(sheet Sheet, i int, mapping Mapping, state Values, label map[Field]string) ([]Change, error) {
	var changes []Change

	for _, e := range Editable {
		col, mapped := mapping.Fields[e.Field]
		if !mapped {
			continue
		}
		cell := sheet.Cell(i, col)
		// An empty cell means "leave it alone", not "set it to empty". A sheet
		// with a blank column would otherwise wipe a field on every extension
		// in it, which is exactly the accident this whole flow exists to catch
		// — and catching it is worse than not causing it.
		if cell == "" {
			continue
		}

		switch e.Field {
		case FieldName:
			if cell != state.Name {
				changes = append(changes, Change{
					Field: e.Field, Label: label[e.Field],
					Before: state.Name, After: cell,
				})
			}
		case FieldEnabled:
			want, err := truth(cell)
			if err != nil {
				return nil, err
			}
			if want != state.Enabled {
				changes = append(changes, Change{
					Field: e.Field, Label: label[e.Field],
					Before: said(state.Enabled), After: said(want),
				})
			}
		}
	}
	return changes, nil
}

// truth reads the many ways a sheet says yes.
func truth(cell string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(cell)) {
	case "true", "yes", "y", "1", "on", "enabled", "active":
		return true, nil
	case "false", "no", "n", "0", "off", "disabled", "inactive":
		return false, nil
	}
	if n, err := strconv.Atoi(cell); err == nil {
		return n != 0, nil
	}
	return false, fmt.Errorf("%q is not a yes or a no", cell)
}

func said(on bool) string {
	if on {
		return "yes"
	}
	return "no"
}

/*
Suggest guesses which column is which from the header names.

Not clever, and deliberately not asked of a model — the file is not shown to
one. It matches the words people actually put at the top of these sheets, and
whatever it gets wrong is corrected in a dropdown before anything is compared.
A wrong guess costs one click; asking somebody to map four columns by hand
every time costs four.
*/
func Suggest(columns []string) Mapping {
	m := Mapping{Extension: -1, Fields: map[Field]int{}}

	for i, raw := range columns {
		name := strings.ToLower(strings.TrimSpace(raw))
		name = strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(name)
		name = strings.Join(strings.Fields(name), " ")

		switch {
		case m.Extension < 0 && matches(name,
			"extension", "extension number", "ext", "ext number", "number", "did"):
			m.Extension = i
		case missing(m.Fields, FieldName) && matches(name,
			"name", "display name", "full name", "display", "user", "user name"):
			m.Fields[FieldName] = i
		case missing(m.Fields, FieldEnabled) && matches(name,
			"enabled", "active", "status", "in use", "on"):
			m.Fields[FieldEnabled] = i
		}
	}
	return m
}

func matches(name string, any ...string) bool {
	for _, candidate := range any {
		if name == candidate {
			return true
		}
	}
	return false
}

func missing(fields map[Field]int, f Field) bool {
	_, ok := fields[f]
	return !ok
}
