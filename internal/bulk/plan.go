package bulk

import (
	"encoding/json"
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

What a sheet can carry is not decided here. The plugin publishes the fields it
will write, and this package builds columns, mappings and comparisons from that
list. It used to hold its own list of two, which is why a sheet could rename an
extension and turn it on and do nothing else, on a phone system that will set
thirty more.
*/

// Field is something a sheet can set: one of Azir's own two, or a name the
// phone system published.
type Field string

const (
	FieldName    Field = "name"
	FieldEnabled Field = "enabled"
)

// Kinds of value a field takes.
const (
	KindText   = "text"
	KindBool   = "bool"
	KindChoice = "choice"
)

// Spec describes one field: what it is called, how it reads, and what it will
// accept.
type Spec struct {
	Field   Field    `json:"field"`
	Label   string   `json:"label"`
	Kind    string   `json:"kind"`
	Group   string   `json:"group"`
	Choices []string `json:"choices,omitempty"`
}

/*
Core is what Azir knows about any phone system, in its own vocabulary.

These two are capability-level ideas rather than one system's field names:
every phone system has something an extension is called and something that
turns it off. They are written through the extension-change capability, and
everything a plugin publishes is written through the options one — which is
why they are kept apart rather than merged into whatever the plugin happens to
call them.
*/
var Core = []Spec{
	{Field: FieldName, Label: "Display name", Kind: KindText, Group: "Basics"},
	{Field: FieldEnabled, Label: "Enabled", Kind: KindBool, Group: "Basics"},
}

/*
Merge puts a plugin's published fields behind Azir's own.

A published field that means the same thing as a core one is dropped — 3CX
publishes "Enabled", which is the same switch FieldEnabled already describes,
and carrying both would put two columns in the sheet that fight over one
setting. Matched on the field name and on the label, case-insensitively,
because a plugin agreeing with Azir about what something is called is exactly
the case worth catching.
*/
func Merge(core []Spec, published []Spec) []Spec {
	taken := map[string]bool{}
	for _, spec := range core {
		taken[strings.ToLower(string(spec.Field))] = true
		taken[strings.ToLower(spec.Label)] = true
	}

	all := append([]Spec{}, core...)
	for _, spec := range published {
		if taken[strings.ToLower(string(spec.Field))] || taken[strings.ToLower(spec.Label)] {
			continue
		}
		if spec.Kind == "" {
			spec.Kind = KindText
		}
		if spec.Group == "" {
			spec.Group = "Other"
		}
		all = append(all, spec)
	}
	return all
}

/*
SheetColumns is the header row Azir writes, and the one it hopes to get back.

Everywhere Azir hands somebody a sheet — the starting list they download, the
undo built from an applied plan — it writes these headers, and Suggest
recognises every one of them. That round trip is the point: download, edit in
Excel, upload, and the columns map themselves.
*/
func SheetColumns(specs []Spec) []string {
	columns := make([]string, 0, len(specs)+1)
	columns = append(columns, "Extension")
	for _, spec := range specs {
		columns = append(columns, spec.Label)
	}
	return columns
}

// CanonicalMapping is what SheetColumns maps to, for the sheets Azir writes
// itself and does not need to guess at.
func CanonicalMapping(specs []Spec) Mapping {
	m := Mapping{Extension: 0, Fields: make(map[Field]int, len(specs))}
	for i, spec := range specs {
		m.Fields[spec.Field] = i + 1
	}
	return m
}

// Line renders one extension as a row under SheetColumns.
func Line(extension string, v Values, specs []Spec) []string {
	line := make([]string, 0, len(specs)+1)
	line = append(line, extension)
	for _, spec := range specs {
		line = append(line, v[spec.Field])
	}
	return line
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

/*
Values is one extension's editable state, as text.

Text rather than a struct per field, because the fields are not known when this
package is compiled — they are whatever the phone system says it will write.
Everything is normalised on the way in, so a comparison is a string comparison
and "true", "Yes" and "1" cannot read as three different settings.
*/
type Values map[Field]string

/*
Normalise puts a cell into the one form this package compares.

A phone system saying true and a spreadsheet saying "Yes" are the same answer,
and finding that out at comparison time rather than at read time is how a plan
ends up full of changes that change nothing.
*/
func Normalise(spec Spec, cell string) (string, error) {
	cell = strings.TrimSpace(cell)
	if cell == "" {
		return "", nil
	}
	switch spec.Kind {
	case KindBool:
		on, err := truth(cell)
		if err != nil {
			return "", err
		}
		return said(on), nil
	case KindChoice:
		for _, choice := range spec.Choices {
			if strings.EqualFold(choice, cell) {
				return choice, nil
			}
		}
		if len(spec.Choices) == 0 {
			return cell, nil
		}
		return "", fmt.Errorf("%q is not one of %s", cell, strings.Join(spec.Choices, ", "))
	default:
		return cell, nil
	}
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
	// Gone marks a row that would remove an extension. Only an undo makes
	// these: a sheet has no way to say "delete this", and giving it one would
	// mean a stray column could take a business's phones off the air.
	Gone bool `json:"gone,omitempty"`
	// Wanted is what a new extension would be called.
	Wanted string `json:"wanted,omitempty"`
	// Problem explains why a row will be skipped: no such extension, an
	// unreadable value, a duplicate. Rows with one are never applied.
	Problem string `json:"problem,omitempty"`
}

// Changed reports whether this row would alter an extension that exists.
func (r Row) Changed() bool { return r.Problem == "" && !r.New && !r.Gone && len(r.Changes) > 0 }

// Creates reports whether this row would make a new extension.
func (r Row) Creates() bool { return r.Problem == "" && r.New }

// Removes reports whether this row would delete an extension.
func (r Row) Removes() bool { return r.Problem == "" && r.Gone }

// Plan is the whole before-and-after, as reviewed and as applied.
type Plan struct {
	Rows []Row `json:"rows"`
	// Counts, so the screen and the confirmation agree without recounting.
	Changing  int `json:"changing"`
	Creating  int `json:"creating"`
	Removing  int `json:"removing"`
	Unchanged int `json:"unchanged"`
	Skipped   int `json:"skipped"`
}

// MarshalJSON writes the rows as an empty list rather than as null, for the
// same reason Sheet does: a plan with nothing in it is a plan, and the screen
// that draws one iterates its rows.
func (p Plan) MarshalJSON() ([]byte, error) {
	type plain Plan
	if p.Rows == nil {
		p.Rows = []Row{}
	}
	return json.Marshal(plain(p))
}

/*
Build compares a mapped sheet against what the system currently reports.

Rows the system has never heard of are kept and marked rather than dropped.
A sheet naming extensions that do not exist is worth seeing — it usually means
the wrong customer, or a column shifted by one — and silently applying the rows
that happened to match would be the worst of both.
*/
func Build(sheet Sheet, mapping Mapping, now Current, specs []Spec) (Plan, error) {
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
			row.Name = state[FieldName]
			changes, err := compare(sheet, i, mapping, state, specs)
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

	plan.Sort()
	return plan, nil
}

/*
Choose builds the same plan from extensions somebody ticked and values they set
in the console, without a sheet in the middle.

The wanted values are per extension, so setting one option across forty and
renaming one of them are the same call. Only extensions the phone system has;
a screen that picks from what is there is the wrong place to invent something
that is not.
*/
func Choose(want map[string]Values, now Current, specs []Spec) Plan {
	var plan Plan
	byField := make(map[Field]Spec, len(specs))
	for _, spec := range specs {
		byField[spec.Field] = spec
	}

	for _, extension := range order(want) {
		state, known := now[extension]
		row := Row{Extension: extension}
		if !known {
			row.Problem = "the phone system has no such extension"
			plan.Skipped++
			plan.Rows = append(plan.Rows, row)
			continue
		}
		row.Name = state[FieldName]

		for _, spec := range specs {
			raw, asked := want[extension][spec.Field]
			if !asked || strings.TrimSpace(raw) == "" {
				continue
			}
			after, err := Normalise(spec, raw)
			if err != nil {
				row.Problem = err.Error()
				break
			}
			if before := state[spec.Field]; after != before {
				row.Changes = append(row.Changes, Change{
					Field: spec.Field, Label: spec.Label, Before: before, After: after,
				})
			}
		}

		switch {
		case row.Problem != "":
			row.Changes = nil
			plan.Skipped++
		case len(row.Changes) > 0:
			plan.Changing++
		default:
			plan.Unchanged++
		}
		plan.Rows = append(plan.Rows, row)
	}

	plan.Sort()
	return plan
}

// order lists extension numbers the way somebody reads them, so 100 comes
// before 1000 rather than after it.
func order(want map[string]Values) []string {
	numbers := make([]string, 0, len(want))
	for number := range want {
		numbers = append(numbers, number)
	}
	sort.Slice(numbers, func(a, b int) bool {
		x, errX := strconv.Atoi(numbers[a])
		y, errY := strconv.Atoi(numbers[b])
		if errX == nil && errY == nil {
			return x < y
		}
		return numbers[a] < numbers[b]
	})
	return numbers
}

// Sort puts the rows in the order somebody reviews them in. Exported because
// an undo appends its removals after Build has run.
func (p *Plan) Sort() {
	sort.SliceStable(p.Rows, func(a, b int) bool {
		return reading(p.Rows[a]) < reading(p.Rows[b])
	})
}

// reading puts the rows that do something first, then the problems, then the
// rows that change nothing — which is the order somebody reviews them in.
func reading(r Row) int {
	switch {
	case r.Changed():
		return 0
	case r.Removes():
		// Above the creates because a removal is the row most worth reading
		// twice, and the only one that cannot be undone by running this again.
		return 1
	case r.Creates():
		return 2
	case r.Problem != "":
		return 3
	default:
		return 4
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
func compare(sheet Sheet, i int, mapping Mapping, state Values, specs []Spec) ([]Change, error) {
	var changes []Change

	for _, spec := range specs {
		col, mapped := mapping.Fields[spec.Field]
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
		after, err := Normalise(spec, cell)
		if err != nil {
			return nil, err
		}
		if before := state[spec.Field]; after != before {
			changes = append(changes, Change{
				Field: spec.Field, Label: spec.Label, Before: before, After: after,
			})
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
one. It matches a field's own label first, which is what Azir writes into every
sheet it hands out, and then the words people actually put at the top of the
ones they write themselves. Whatever it gets wrong is corrected in a dropdown
before anything is compared: a wrong guess costs one click, and mapping thirty
columns by hand every time costs thirty.
*/
func Suggest(columns []string, specs []Spec) Mapping {
	m := Mapping{Extension: -1, Fields: map[Field]int{}}

	// The words somebody writes at the top of a sheet of their own, for the
	// two fields that predate any plugin. A published field is matched on its
	// label and its name, which is all Azir knows about it.
	aliases := map[Field][]string{
		FieldName:    {"name", "display name", "full name", "display", "user", "user name"},
		FieldEnabled: {"enabled", "active", "status", "in use", "on"},
	}

	for i, raw := range columns {
		name := tidy(raw)
		if name == "" {
			continue
		}
		if m.Extension < 0 && matches(name,
			"extension", "extension number", "ext", "ext number", "number", "did") {
			m.Extension = i
			continue
		}
		for _, spec := range specs {
			if _, already := m.Fields[spec.Field]; already {
				continue
			}
			if name == tidy(spec.Label) || name == tidy(string(spec.Field)) ||
				matches(name, aliases[spec.Field]...) {
				m.Fields[spec.Field] = i
				break
			}
		}
	}
	return m
}

// tidy reduces a header to the form headers are compared in, so "Display_Name"
// and "display name" are the same column.
func tidy(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	name = strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(name)
	return strings.Join(strings.Fields(name), " ")
}

func matches(name string, any ...string) bool {
	for _, candidate := range any {
		if name == candidate {
			return true
		}
	}
	return false
}
