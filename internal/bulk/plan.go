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
	/*
		KindDestination is where a call goes: voicemail, an extension, an
		outside number, nothing.

		Written as one string — "VoiceMail", "Extension:101", "External:5551234"
		— so that it is a field like any other. That is what lets a forwarding
		rule appear in a sheet, in a diff and in a bulk edit without any of them
		learning what a destination is; "send these forty to voicemail while
		they are out" is the same machinery as any other change.
	*/
	KindDestination = "destination"
	/*
		KindReadOnly is something worth seeing and not something Azir will
		change — a department, which the phone system holds as group
		membership rather than as a value on the extension.

		Shown in the form and left out of sheets and comparisons, because a
		column somebody can fill in and Azir will ignore is worse than no
		column at all.
	*/
	KindReadOnly = "readonly"
	// KindSecret is a credential — a voicemail PIN. Written, never read.
	//
	// Deliberately outside everything in this package. A plan is stored in the
	// database, shown on a screen, kept in the activity log and handed back as
	// an undo, and a value that goes through all of that is a credential at
	// rest in four places. There is also nothing to compare it against, since
	// nothing reads it: every "change" would be a change. Secrets are set as
	// their own action on one extension, not staged in a batch.
	KindSecret = "secret"
)

// Comparable reports whether a field can take part in a before-and-after.
// A secret has nothing to compare against and a read-only field has nothing to
// change, so neither belongs in a diff.
func Comparable(spec Spec) bool {
	return spec.Kind != KindSecret && spec.Kind != KindReadOnly
}

/*
Sheetable reports whether a field may appear in a sheet Azir writes.

Wider than Comparable by exactly the read-only fields, and that is the whole
difference between the two: a MAC address or the DID assigned to an extension
is worth carrying in an export beside the things somebody is about to edit,
even though Azir will never write it back.

The rule that used to keep these out — a column somebody can fill in and Azir
will ignore is worse than no column at all — is kept by marking the header
rather than by dropping the column. SheetColumns writes them as "(read-only)",
and Suggest refuses to map one on the way back in, so a sheet that comes home
with edits in that column is not half-applied: it is not applied at all, and
the header said so before anybody typed.

Secrets stay out under every circumstance. A voicemail PIN in a downloaded file
is a credential at rest in somebody's downloads folder.
*/
func Sheetable(spec Spec) bool {
	return spec.Kind != KindSecret
}

// Spec describes one field: what it is called, how it reads, and what it will
// accept.
type Spec struct {
	Field   Field    `json:"field"`
	Label   string   `json:"label"`
	Kind    string   `json:"kind"`
	Group   string   `json:"group"`
	Choices []string `json:"choices,omitempty"`
	// Labels is what to show for each choice, where the phone system's own
	// value is not a thing to put in front of a person. A role is stored as
	// "system_owners" and read as "System Owner".
	//
	// By value rather than positional, so a choice with no entry simply shows
	// as itself and a list can grow without the two falling out of step.
	Labels map[string]string `json:"labels,omitempty"`
	// States is how each choice is doing, where that is a thing a choice can
	// be. A routing device that is not connected is still a choice; it is one
	// somebody should make on purpose rather than by accident.
	States map[string]string `json:"states,omitempty"`
	/*
		Unique marks a field no two extensions may share — an email address.

		Published by the plugin, because which fields those are is a fact about
		the phone system and not something Azir can know. What Azir does with
		it is refuse to stage a bulk edit that would set one value on several
		extensions, which is the only way that edit can end: the first
		extension takes it and the rest are refused, one at a time, after the
		first has already been written.
	*/
	Unique bool `json:"unique,omitempty"`
	// Sheeted marks a field that belongs in a spreadsheet and not in a form.
	//
	// One field so far: the display name, where the phone system also offers
	// the parts of it. A sheet has always had one column for a name and should
	// keep it; a form offering both the whole and the parts is two controls
	// fighting over one setting.
	SheetOnly bool `json:"sheet_only,omitempty"`
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
	{Field: FieldName, Label: "Display name", Kind: KindText, Group: "General"},
	{Field: FieldEnabled, Label: "Enabled", Kind: KindBool, Group: "General"},
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

	// Where the phone system offers the parts of a name, the display name
	// stops being something a form should offer. Decided here rather than in
	// whatever draws the form, so it is decided once and can be tested.
	splits := Splits(published)
	all := make([]Spec, 0, len(core)+len(published))
	for _, spec := range core {
		if splits && spec.Field == FieldName {
			spec.SheetOnly = true
		}
		all = append(all, spec)
	}
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
	sheeted := Sheeted(specs)
	columns := make([]string, 0, len(sheeted)+1)
	columns = append(columns, "Extension")
	for _, spec := range sheeted {
		columns = append(columns, ColumnName(spec))
	}
	return columns
}

// ColumnName is a field's header in a sheet. Read-only fields say so in the
// header, because that is the only place somebody sees it before they start
// typing into the column.
func ColumnName(spec Spec) string {
	if !Comparable(spec) {
		return spec.Label + " (read-only)"
	}
	return spec.Label
}

// Sheeted is the fields a sheet can carry, which is every field but the
// secrets. Exported because SheetColumns, CanonicalMapping and Line all have
// to agree on the order, and one function answering for all three is what
// stops them drifting a column apart.
func Sheeted(specs []Spec) []Spec {
	kept := make([]Spec, 0, len(specs))
	for _, spec := range specs {
		if Sheetable(spec) {
			kept = append(kept, spec)
		}
	}
	return kept
}

// CanonicalMapping is what SheetColumns maps to, for the sheets Azir writes
// itself and does not need to guess at.
func CanonicalMapping(specs []Spec) Mapping {
	m := Mapping{Extension: 0, Fields: make(map[Field]int, len(specs))}
	for i, spec := range Sheeted(specs) {
		m.Fields[spec.Field] = i + 1
	}
	return m
}

// Line renders one extension as a row under SheetColumns.
func Line(extension string, v Values, specs []Spec) []string {
	sheeted := Sheeted(specs)
	line := make([]string, 0, len(sheeted)+1)
	line = append(line, extension)
	for _, spec := range sheeted {
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
	case KindDestination:
		where, number, _ := strings.Cut(cell, ":")
		where, number = strings.TrimSpace(where), strings.TrimSpace(number)
		for _, choice := range spec.Choices {
			if !strings.EqualFold(choice, where) {
				continue
			}
			// Somewhere that does not take a number is written without one,
			// whatever came with it. The phone system keeps the extension's
			// own number beside a voicemail rule — it is whose voicemail it
			// is, not where the call goes — and carrying it would make
			// "VoiceMail" and "VoiceMail:100" two different answers to the
			// same question, so every comparison would find a change.
			if !NeedsNumber(choice) {
				return choice, nil
			}
			// Somewhere that needs a number and has not been given one is a
			// rule that would send a call nowhere.
			if number == "" {
				return "", fmt.Errorf("%s needs a number to send calls to", choice)
			}
			return choice + ":" + number, nil
		}
		return "", fmt.Errorf("%q is not somewhere calls can go: %s",
			where, strings.Join(spec.Choices, ", "))
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
	// A unique field given one value across several extensions is settled here
	// rather than refused: each extension gets its own address.
	want = disambiguate(want, now, specs)

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
			if !Comparable(spec) {
				continue
			}
			raw, asked := want[extension][spec.Field]
			if !asked {
				continue
			}
			// Unlike a sheet, where a blank cell is a column somebody did not
			// fill in, a field is here only because somebody set it. So an
			// empty one is an answer — clear it — for the kinds that can hold
			// nothing. A picker cannot produce an empty choice or an empty
			// yes-or-no, so for those it still means "leave it alone".
			if strings.TrimSpace(raw) == "" && spec.Kind != KindText {
				continue
			}
			if spec.Unique && collides(extension, spec.Field, raw, now) {
				row.Problem = fmt.Sprintf(
					"another extension already has that %s, and it is not an address this can tell apart",
					strings.ToLower(spec.Label))
				break
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

/*
disambiguate gives each extension its own value where the phone system insists
they differ.

An email address belongs to one extension and 3CX enforces it, so setting one
across five is not an edit that half works — the first takes it and the rest
are refused, individually, after the first has already been written. That was
caught and refused outright, which was correct and unhelpful: "give these five
an address" is a real thing to want, and the extensions are what make them
distinguishable.

So the address gets a tag naming the extension. reception@example.com becomes
reception+101@example.com, reception+102@example.com, and so on — one address
to a mailbox, delivered to the same place, and the extension is right there in
it. Only where it is needed: an extension that already holds the address keeps
it unchanged, and a value nothing else claims is used as it was typed.

Only addresses. Plus-addressing means something for an email and nothing for
anything else, so a unique field carrying some other kind of value is left for
the caller to sort out, and Choose reports the collision rather than inventing
a value it has no rule for.
*/
func disambiguate(want map[string]Values, now Current, specs []Spec) map[string]Values {
	var unique []Spec
	for _, spec := range specs {
		if spec.Unique && Comparable(spec) {
			unique = append(unique, spec)
		}
	}
	if len(unique) == 0 {
		return want
	}

	out := want
	copied := false
	for _, spec := range unique {
		// Every value this field already holds, so an address is checked
		// against the whole phone system and not merely against this batch.
		// The one belonging to the extension being changed does not count
		// against it.
		taken := map[string]string{}
		for extension, values := range now {
			if value := strings.ToLower(strings.TrimSpace(values[spec.Field])); value != "" {
				taken[value] = extension
			}
		}

		// How many extensions in this batch are asking for each value. Two or
		// more and every one of them is tagged, rather than whichever sorted
		// first keeping the plain address and the rest looking like an
		// afterthought. Setting one address on one extension is untouched.
		asking := map[string]int{}
		for _, values := range want {
			if value := strings.ToLower(strings.TrimSpace(values[spec.Field])); value != "" {
				asking[value]++
			}
		}

		for _, extension := range order(want) {
			raw := strings.TrimSpace(want[extension][spec.Field])
			if raw == "" {
				continue
			}
			holder, claimed := taken[strings.ToLower(raw)]
			// The extension that already has the address keeps it exactly as
			// it is. Tagging it would be a change nobody asked for, on the one
			// extension that was already right.
			if holder == extension {
				continue
			}
			if !claimed && asking[strings.ToLower(raw)] < 2 {
				taken[strings.ToLower(raw)] = extension
				continue
			}
			tagged, ok := tag(raw, extension)
			if !ok {
				// Not an address. Left alone, and Choose refuses it.
				continue
			}
			if !copied {
				out = deepen(want)
				copied = true
			}
			out[extension][spec.Field] = tagged
			taken[strings.ToLower(tagged)] = extension
		}
	}
	return out
}

// collides reports whether a value is already held by a different extension.
// Reached only for a unique field that could not be told apart.
func collides(extension string, field Field, raw string, now Current) bool {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return false
	}
	for other, values := range now {
		if other == extension {
			continue
		}
		if strings.ToLower(strings.TrimSpace(values[field])) == raw {
			return true
		}
	}
	return false
}

/*
tag puts the extension into an address, the way a mailbox already understands.

"reception@example.com" and 101 become "reception+101@example.com". Anything
that is not an address is refused rather than mangled, and an address that
already carries this extension's tag is left exactly as it is.
*/
func tag(address, extension string) (string, bool) {
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return "", false
	}
	local, domain := address[:at], address[at+1:]
	if strings.HasSuffix(local, "+"+extension) {
		return address, true
	}
	return local + "+" + extension + "@" + domain, true
}

// deepen copies the wanted values, so disambiguating never edits the caller's
// map. The rows arrive sharing one map per extension where a console set the
// same fields on all of them, and writing through that would give every
// extension the last one's address.
func deepen(want map[string]Values) map[string]Values {
	out := make(map[string]Values, len(want))
	for extension, values := range want {
		copied := make(Values, len(values))
		for field, value := range values {
			copied[field] = value
		}
		out[extension] = copied
	}
	return out
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
		if !Comparable(spec) {
			continue
		}
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

/*
NeedsNumber reports whether a destination is incomplete without one.

Voicemail and nothing are complete on their own; an extension or an outside
number is a place, and a place with no address sends calls into silence.

Exported because a plugin reading a destination off a phone system has to give
the same answer as this does reading one somebody typed. When they disagreed —
and they did, as two identical copies that drifted apart the moment one was
edited — a rule read as "VoiceMail" and stored as "VoiceMail:100" was a change
on every extension, on every comparison, forever.
*/
func NeedsNumber(where string) bool {
	switch where {
	case "Extension", "External", "Queue", "RingGroup", "IVR", "Fax", "RoutePoint":
		return true
	}
	return false
}

// Where splits a destination into what kind of place it is and which one.
func Where(value string) (where, number string) {
	where, number, _ = strings.Cut(value, ":")
	return strings.TrimSpace(where), strings.TrimSpace(number)
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
			if !Comparable(spec) {
				continue
			}
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
