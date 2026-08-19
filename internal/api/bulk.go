package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/bulk"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/pkg/plugin"
)

/*
Changing many extensions from a sheet.

Three steps on purpose, because the middle one is the point. A file is
uploaded and parsed; it is compared against what the phone system says right
now; and a person approves the difference. Nothing is applied from what the
sheet said alone.

The file never reaches the model. It is read here, by Azir, with the columns
mapped in the console — a customer's extension list is their data, and the rule
everywhere else in this system is that it does not leave for a model to read.
The assistant plays no part in this path.

Guarded by phone.manage throughout, including the reading. Changing a
customer's phone system is the permission this is under, and somebody who
cannot do it one extension at a time has no business staging forty of them.
*/

// uploadBulk parses a sheet and holds it for mapping.
func (s *Server) uploadBulk(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("customer_id")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("say which customer this is for"))
		return
	}
	if _, err := s.DB.GetCustomer(r.Context(), customerID); err != nil {
		writeJSON(w, http.StatusNotFound, errBody("no such customer"))
		return
	}

	if err := r.ParseMultipartForm(bulk.MaxBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("that upload could not be read"))
		return
	}
	file, header, err := r.FormFile("sheet")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("attach the sheet as 'sheet'"))
		return
	}
	defer file.Close() //nolint:errcheck // read-only

	sheet, err := bulk.Parse(file)
	if err != nil {
		// These messages are written for the person who chose the file.
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}

	encoded, err := json.Marshal(sheet)
	if err != nil {
		s.fail(w, err, "could not read that sheet")
		return
	}
	saved, err := s.DB.AddBulkEdit(r.Context(), store.BulkEdit{
		CustomerID: customerID,
		Filename:   header.Filename,
		UploadedBy: actor.Email,
		Sheet:      encoded,
	})
	if err != nil {
		s.fail(w, err, "could not store that sheet")
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "bulk.upload",
		Outcome:     audit.OutcomeOK,
		CustomerID:  &customerID,
		Detail:      fmt.Sprintf("%s, %d rows", header.Filename, len(sheet.Rows)),
	})

	specs := s.specsFor(r.Context(), actor, customerID)
	writeJSON(w, http.StatusOK, map[string]any{
		"edit":     saved,
		"columns":  sheet.Columns,
		"suggests": bulk.Suggest(sheet.Columns, specs),
		"editable": specs,
	})
}

// planBulk compares a mapped sheet against the phone system as it is now.
func (s *Server) planBulk(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	edit, ok := s.bulkEdit(w, r)
	if !ok {
		return
	}

	var mapping bulk.Mapping
	if err := decode(r, &mapping); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	var sheet bulk.Sheet
	if err := json.Unmarshal(edit.Sheet, &sheet); err != nil {
		s.fail(w, err, "that sheet could not be read back")
		return
	}

	now, specs, err := s.currentExtensions(r.Context(), actor, edit.CustomerID)
	if err != nil {
		// Worth being plain about: without the current state there is no
		// before, and a plan with no before is just the sheet again.
		writeJSON(w, http.StatusBadGateway, errBody(
			"could not read the phone system, so there is nothing to compare against: "+err.Error()))
		return
	}

	plan, err := bulk.Build(sheet, mapping, now, specs)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}

	encodedMapping, _ := json.Marshal(mapping)
	encodedPlan, err := json.Marshal(plan)
	if err != nil {
		s.fail(w, err, "could not store that plan")
		return
	}
	saved, err := s.DB.SaveBulkPlan(r.Context(), edit.ID, encodedMapping, encodedPlan)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusConflict, errBody("that sheet has already been decided"))
			return
		}
		s.fail(w, err, "could not store that plan")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"edit": saved, "plan": plan})
}

// getBulk reads one back, plan and all.
func (s *Server) getBulk(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("that is not a bulk edit id"))
		return
	}
	edit, err := s.DB.BulkEditByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errBody("no such bulk edit"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"edit": edit, "editable": s.specsFor(r.Context(), actor, edit.CustomerID)})
}

// listBulk returns a customer's recent sheets.
func (s *Server) listBulk(w http.ResponseWriter, r *http.Request) {
	customerID, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("customer_id")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("say which customer"))
		return
	}
	edits, err := s.DB.ListBulkEdits(r.Context(), customerID, 20)
	if err != nil {
		s.fail(w, err, "could not list the bulk edits")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"edits": edits})
}

// applied is what happened to one row.
type applied struct {
	Extension string `json:"extension"`
	OK        bool   `json:"ok"`
	New       bool   `json:"new,omitempty"`
	Gone      bool   `json:"gone,omitempty"`
	// Left marks a row somebody unticked before approving. Recorded rather
	// than dropped: "we left that one alone" and "that one never came up" look
	// identical afterwards otherwise.
	Left    bool   `json:"left,omitempty"`
	Problem string `json:"problem,omitempty"`
}

/*
applyBulk makes the changes a person approved.

Every row is attempted and every row is reported. Stopping at the first failure
would leave somebody not knowing which of forty extensions had been changed,
which is worse than the failure itself — the same reasoning the plugin's own
bulk update follows.

Only rows that would change something are touched. A sheet of two hundred
extensions where three differ makes three requests, not two hundred.
*/
func (s *Server) applyBulk(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	edit, ok := s.bulkEdit(w, r)
	if !ok {
		return
	}
	if edit.Status != "planned" {
		writeJSON(w, http.StatusConflict, errBody(
			"there is nothing to apply — check the before and after first"))
		return
	}

	var body struct {
		Confirm string `json:"confirm"`
		// Skip is the extensions somebody unticked in the before-and-after.
		// A plan is what was compared; this is what they chose to do about
		// it, which is not always all of it.
		Skip []string `json:"skip"`
		/*
			Secrets are the write-only fields, sent now rather than staged.

			A voicemail PIN cannot go in a plan. A plan is written to the
			database, drawn on a screen, kept in the activity log and handed
			back as an undo — four places a credential would then be at rest,
			for a value nothing ever reads back and nothing can compare
			against. So it is not compared: it travels from the form to the
			phone system at the moment somebody confirms, and is held nowhere
			in between.

			Which is why it arrives here and not in the plan this is applying.
		*/
		Secrets map[string]string `json:"secrets"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	if !strings.EqualFold(strings.TrimSpace(body.Confirm), "apply") {
		writeJSON(w, http.StatusBadRequest, errBody(
			`Type "apply" to confirm. Nothing has been changed.`))
		return
	}

	var plan bulk.Plan
	if err := json.Unmarshal(edit.Plan, &plan); err != nil {
		s.fail(w, err, "that plan could not be read back")
		return
	}

	left := make(map[string]bool, len(body.Skip))
	for _, extension := range body.Skip {
		left[extension] = true
	}

	specs := s.specsFor(r.Context(), actor, edit.CustomerID)
	byField := make(map[bulk.Field]bulk.Spec, len(specs))
	for _, spec := range specs {
		byField[spec.Field] = spec
	}

	// Outcomes by extension rather than a list built as it goes, because the
	// option changes are sent in batches below and have to find their way back
	// to the row they came from.
	outcome := map[string]*applied{}
	var order []string
	note := func(row bulk.Row, was applied) *applied {
		if got, seen := outcome[row.Extension]; seen {
			return got
		}
		copied := was
		outcome[row.Extension] = &copied
		order = append(order, row.Extension)
		return &copied
	}

	// Option changes are grouped by what they set. A dialog that turns call
	// recording on for forty extensions is one request, not forty, because
	// the phone system's own bulk endpoint takes a list — and it answers per
	// extension, so the grouping costs nothing in what can be reported.
	type batch struct {
		settings   map[string]any
		extensions []string
	}
	batches := map[string]*batch{}
	var batchOrder []string

	var made, gone, gaveUp int
	for _, row := range plan.Rows {
		if left[row.Extension] && (row.Changed() || row.Creates() || row.Removes()) {
			note(row, applied{Extension: row.Extension, Left: true})
			gaveUp++
			continue
		}
		switch {
		case row.Removes():
			at := note(row, applied{Extension: row.Extension, Gone: true})
			if err := s.removeRow(r.Context(), actor, edit.CustomerID, row); err != nil {
				at.Problem = err.Error()
				continue
			}
			at.OK = true
			gone++
		case row.Creates():
			at := note(row, applied{Extension: row.Extension, New: true})
			if err := s.createRow(r.Context(), actor, edit.CustomerID, row); err != nil {
				at.Problem = err.Error()
				continue
			}
			at.OK = true
			made++
		case row.Changed():
			at := note(row, applied{Extension: row.Extension})
			core, options := splitChanges(row.Changes)

			if len(core) > 0 {
				if err := s.applyRow(r.Context(), actor, edit.CustomerID, row.Extension, core); err != nil {
					at.Problem = err.Error()
					continue
				}
				at.OK = true
			}
			if len(options) == 0 {
				continue
			}

			settings := settingsFor(options, byField)
			key, err := json.Marshal(settings)
			if err != nil {
				at.Problem = "those settings could not be sent"
				at.OK = false
				continue
			}
			group, seen := batches[string(key)]
			if !seen {
				group = &batch{settings: settings}
				batches[string(key)] = group
				batchOrder = append(batchOrder, string(key))
			}
			group.extensions = append(group.extensions, row.Extension)
		}
	}

	/*
		The write-only fields, applied to everything somebody chose.

		Not only to the rows that would change: there is nothing to compare a
		PIN against, so "these five extensions differ" has no meaning for one
		and every extension in the batch is meant. A row that was skipped for a
		real problem is left alone, and so is one being removed — setting a
		voicemail PIN on an extension about to be deleted is work nobody wants
		done.
	*/
	if len(body.Secrets) > 0 {
		settings, err := secretsFor(body.Secrets, byField)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
			return
		}
		var on []string
		for _, row := range plan.Rows {
			if left[row.Extension] || row.Problem != "" || row.Removes() || row.Creates() {
				continue
			}
			on = append(on, row.Extension)
		}
		if len(on) > 0 {
			said, err := s.setOptions(r.Context(), actor, edit.CustomerID, on, settings)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
				return
			}
			// Reported per extension like everything else. Without this a
			// batch that only sets a PIN answered "0 changed, 0 failed" with
			// an empty list — the write had happened, and the screen said
			// nothing at all had.
			for _, extension := range on {
				at := outcome[extension]
				if at == nil {
					at = note(bulk.Row{Extension: extension}, applied{Extension: extension})
				}
				if why := said[extension]; why != "" {
					at.OK = false
					at.Problem = why
					continue
				}
				if at.Problem == "" {
					at.OK = true
				}
			}
		}
	}

	for _, key := range batchOrder {
		group := batches[key]
		said, err := s.setOptions(r.Context(), actor, edit.CustomerID, group.extensions, group.settings)
		for _, extension := range group.extensions {
			at := outcome[extension]
			if at == nil {
				continue
			}
			switch {
			case err != nil:
				at.OK = false
				at.Problem = err.Error()
			case said[extension] != "":
				at.OK = false
				at.Problem = said[extension]
			default:
				at.OK = true
			}
		}
	}

	// made and gone are only ever counted on success above, so failures are
	// counted here once, whatever kind of row they were.
	results := make([]applied, 0, len(order))
	var done, failed int
	for _, extension := range order {
		at := outcome[extension]
		results = append(results, *at)
		switch {
		case at.Left:
		case !at.OK:
			failed++
		case at.New, at.Gone:
			// Already counted as made or gone.
		default:
			done++
		}
	}

	encoded, _ := json.Marshal(results)
	saved, err := s.DB.FinishBulkEdit(r.Context(), edit.ID, "applied", actor.Email, encoded)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Somebody else pressed it, or this request was retried. The
			// changes above are done either way, so say so rather than
			// pretending nothing happened.
			writeJSON(w, http.StatusConflict, errBody(
				"that sheet was already applied. Check the phone system before applying anything else."))
			return
		}
		s.fail(w, err, "the changes were made but could not be recorded")
		return
	}

	customerID := edit.CustomerID
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "bulk.apply",
		Outcome:     audit.OutcomeOK,
		CustomerID:  &customerID,
		Detail: fmt.Sprintf("%s: %d changed, %d created, %d removed, %d left alone, %d failed%s",
			edit.Filename, done, made, gone, gaveUp, failed, alsoSet(body.Secrets)),
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"edit": saved, "results": results,
		"changed": done, "created": made, "removed": gone,
		"left": gaveUp, "failed": failed,
	})
}

// cancelBulk throws one away without applying it.
func (s *Server) cancelBulk(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	edit, ok := s.bulkEdit(w, r)
	if !ok {
		return
	}
	saved, err := s.DB.FinishBulkEdit(r.Context(), edit.ID, "cancelled", actor.Email, nil)
	if err != nil {
		writeJSON(w, http.StatusConflict, errBody("that sheet has already been decided"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"edit": saved})
}

// bulkEdit resolves the one named in the path.
func (s *Server) bulkEdit(w http.ResponseWriter, r *http.Request) (store.BulkEdit, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("that is not a bulk edit id"))
		return store.BulkEdit{}, false
	}
	edit, err := s.DB.BulkEditByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errBody("no such bulk edit"))
		return store.BulkEdit{}, false
	}
	return edit, true
}

/*
listExtensions hands back what the phone system says right now, and what can be
changed about it.

The screen that picks extensions to change needs the same list the comparison
uses, so it comes from the same call. Somebody choosing from a list of what is
actually there cannot pick an extension that does not exist, mistype a number,
or aim a sheet at the wrong customer — three of the four ways this feature
could go wrong before it has started.
*/
func (s *Server) listExtensions(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("customer_id")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("say which customer"))
		return
	}
	now, specs, err := s.currentExtensions(r.Context(), actor, customerID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody("could not read the phone system: "+err.Error()))
		return
	}

	type extension struct {
		Extension string            `json:"extension"`
		Name      string            `json:"name"`
		Enabled   bool              `json:"enabled"`
		Values    map[string]string `json:"values"`
	}
	list := make([]extension, 0, len(now))
	for _, number := range inOrder(now) {
		values := make(map[string]string, len(now[number]))
		for field, text := range now[number] {
			values[string(field)] = text
		}
		list = append(list, extension{
			Extension: number,
			Name:      now[number][bulk.FieldName],
			Enabled:   now[number][bulk.FieldEnabled] != "no",
			Values:    values,
		})
	}

	// The fields travel with the values, so the console can offer them as
	// columns and as a form without keeping its own copy of the answer.
	writeJSON(w, http.StatusOK, map[string]any{
		"extensions": list,
		"fields":     specs,
		"columns":    bulk.SheetColumns(specs),
	})
}

/*
startingSheet writes the customer's extensions as the sheet to edit.

The alternative was a page that says "upload a CSV" and leaves somebody to work
out which columns it wants. Handing them the file instead answers the question
by construction: the headers are the ones Suggest recognises, the rows are what
is true today, and every cell left alone means exactly that. Editing what you
were given is also the only version of this where the extension numbers are
guaranteed right.

Which columns is asked rather than assumed. A phone system with thirty-two
settings makes a thirty-three column sheet, which is correct and unusable;
naming the handful somebody came to change makes a sheet they can read. Left
unsaid, they all come, because a download that quietly dropped the column
somebody needed would be worse than a wide file.
*/
func (s *Server) startingSheet(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("customer_id")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("say which customer"))
		return
	}
	customer, err := s.DB.GetCustomer(r.Context(), customerID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errBody("no such customer"))
		return
	}
	now, specs, err := s.currentExtensions(r.Context(), actor, customerID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody("could not read the phone system: "+err.Error()))
		return
	}
	specs = onlyAsked(specs, r.URL.Query().Get("fields"))
	if len(specs) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody("choose at least one column"))
		return
	}

	var out strings.Builder
	sheet := csv.NewWriter(&out)
	_ = sheet.Write(bulk.SheetColumns(specs))
	for _, number := range inOrder(now) {
		_ = sheet.Write(bulk.Line(number, now[number], specs))
	}
	sheet.Flush()
	if err := sheet.Error(); err != nil {
		s.fail(w, err, "could not write that sheet")
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s-extensions.csv"`, fileWord(customer.DisplayName)))
	// Nothing here is worth a second copy in a proxy or a browser cache: it is
	// a customer's extension list, and it is stale the moment it is written.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, out.String())
}

// onlyAsked narrows the fields to the ones named, keeping the published order
// so two downloads of the same choice are the same file. An empty ask means
// all of them.
func onlyAsked(specs []bulk.Spec, asked string) []bulk.Spec {
	asked = strings.TrimSpace(asked)
	if asked == "" {
		return specs
	}
	wanted := map[string]bool{}
	for _, name := range strings.Split(asked, ",") {
		wanted[strings.TrimSpace(name)] = true
	}
	kept := make([]bulk.Spec, 0, len(wanted))
	for _, spec := range specs {
		if wanted[string(spec.Field)] {
			kept = append(kept, spec)
		}
	}
	return kept
}

/*
planChosen turns extensions ticked in the console into the same reviewed plan a
file gets.

Deliberately the same road: it is written out as a sheet, stored as a sheet,
and compared as a sheet. Renaming four people should not need a spreadsheet,
but neither should it get a shortcut past the comparison — the diff, the
approval, the audit line and the undo are all downstream of the plan, and there
is one plan.

No creating here. A row you ticked exists by definition, and a screen that
picks from what is there is the wrong place to invent something that is not.
*/
func (s *Server) planChosen(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("customer_id")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("say which customer this is for"))
		return
	}
	if _, err := s.DB.GetCustomer(r.Context(), customerID); err != nil {
		writeJSON(w, http.StatusNotFound, errBody("no such customer"))
		return
	}

	var body struct {
		// Wanted is what each ticked extension should say, keyed by extension
		// number then by field. A field left out is left alone.
		Wanted map[string]map[string]string `json:"wanted"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	if len(body.Wanted) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody("choose at least one extension"))
		return
	}
	if len(body.Wanted) > bulk.MaxRows {
		writeJSON(w, http.StatusBadRequest, errBody("that is more extensions than this will take at once"))
		return
	}

	now, specs, err := s.currentExtensions(r.Context(), actor, customerID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(
			"could not read the phone system, so there is nothing to compare against: "+err.Error()))
		return
	}

	want := make(map[string]bulk.Values, len(body.Wanted))
	for extension, fields := range body.Wanted {
		values := make(bulk.Values, len(fields))
		for field, text := range fields {
			values[bulk.Field(field)] = text
		}
		want[strings.TrimSpace(extension)] = values
	}
	plan := bulk.Choose(want, now, specs)

	// Written out as a sheet as well, so what was decided reads the same way
	// however it was made, and so the undo has something to build from.
	sheet := bulk.Sheet{Columns: bulk.SheetColumns(specs)}
	for _, extension := range inOrderOf(want) {
		sheet.Rows = append(sheet.Rows, bulk.Line(extension, want[extension], specs))
	}

	encodedSheet, err := json.Marshal(sheet)
	if err != nil {
		s.fail(w, err, "could not store those changes")
		return
	}
	saved, err := s.DB.AddBulkEdit(r.Context(), store.BulkEdit{
		CustomerID: customerID,
		Filename:   "chosen in Azir",
		UploadedBy: actor.Email,
		Sheet:      encodedSheet,
	})
	if err != nil {
		s.fail(w, err, "could not store those changes")
		return
	}

	mapping := bulk.CanonicalMapping(specs)
	encodedMapping, _ := json.Marshal(mapping)
	encodedPlan, err := json.Marshal(plan)
	if err != nil {
		s.fail(w, err, "could not store that plan")
		return
	}
	planned, err := s.DB.SaveBulkPlan(r.Context(), saved.ID, encodedMapping, encodedPlan)
	if err != nil {
		s.fail(w, err, "could not store that plan")
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "bulk.chosen",
		Outcome:     audit.OutcomeOK,
		CustomerID:  &customerID,
		Detail:      fmt.Sprintf("%d chosen, %d would change", len(want), plan.Changing),
	})

	writeJSON(w, http.StatusOK, map[string]any{"edit": planned, "plan": plan})
}

// inOrderOf sorts the extensions somebody chose the way they read.
func inOrderOf(want map[string]bulk.Values) []string {
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

/*
removeRow deletes one extension.

Only ever reached from an undo. Named one at a time even though the tool takes
a list, so a batch that half fails says which numbers are still there.
*/
func (s *Server) removeRow(ctx context.Context, actor identity.Actor, customer uuid.UUID, row bulk.Row) error {
	tool, err := s.approvedTool(ctx, plugin.CapPhoneExtensionDelete, true)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(map[string]any{"extensions": []string{row.Extension}})
	if err != nil {
		return err
	}
	if _, err := s.performTool(ctx, actor, tool, encoded, &customer, fromSheet); err != nil {
		return err
	}
	return nil
}

// inOrder sorts extension numbers the way somebody reads them, so 100 comes
// before 1000 rather than after it.
func inOrder(now bulk.Current) []string {
	numbers := make([]string, 0, len(now))
	for number := range now {
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

// fileWord turns a customer's name into something safe to put in a filename on
// any of the three operating systems somebody might open it on.
func fileWord(name string) string {
	var out strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			dash = false
		case !dash && out.Len() > 0:
			out.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(out.String(), "-")
}
