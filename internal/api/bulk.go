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
	"github.com/dreulavelle/azir/internal/registry"
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

	writeJSON(w, http.StatusOK, map[string]any{
		"edit":     saved,
		"columns":  sheet.Columns,
		"suggests": bulk.Suggest(sheet.Columns),
		"editable": bulk.Editable,
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

	now, err := s.currentExtensions(r.Context(), actor, edit.CustomerID)
	if err != nil {
		// Worth being plain about: without the current state there is no
		// before, and a plan with no before is just the sheet again.
		writeJSON(w, http.StatusBadGateway, errBody(
			"could not read the phone system, so there is nothing to compare against: "+err.Error()))
		return
	}

	plan, err := bulk.Build(sheet, mapping, now)
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
func (s *Server) getBulk(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, map[string]any{"edit": edit, "editable": bulk.Editable})
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

	results := make([]applied, 0, plan.Changing+plan.Creating+plan.Removing)
	var done, made, gone, gaveUp, failed int
	for _, row := range plan.Rows {
		if left[row.Extension] && (row.Changed() || row.Creates() || row.Removes()) {
			results = append(results, applied{Extension: row.Extension, Left: true})
			gaveUp++
			continue
		}
		switch {
		case row.Removes():
			if err := s.removeRow(r.Context(), actor, edit.CustomerID, row); err != nil {
				results = append(results, applied{Extension: row.Extension, Gone: true, Problem: err.Error()})
				failed++
				continue
			}
			results = append(results, applied{Extension: row.Extension, Gone: true, OK: true})
			gone++
		case row.Changed():
			if err := s.applyRow(r.Context(), actor, edit.CustomerID, row); err != nil {
				results = append(results, applied{Extension: row.Extension, Problem: err.Error()})
				failed++
				continue
			}
			results = append(results, applied{Extension: row.Extension, OK: true})
			done++
		case row.Creates():
			if err := s.createRow(r.Context(), actor, edit.CustomerID, row); err != nil {
				results = append(results, applied{Extension: row.Extension, New: true, Problem: err.Error()})
				failed++
				continue
			}
			results = append(results, applied{Extension: row.Extension, New: true, OK: true})
			made++
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
		Detail: fmt.Sprintf("%s: %d changed, %d created, %d removed, %d left alone, %d failed",
			edit.Filename, done, made, gone, gaveUp, failed),
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
currentExtensions reads what the phone system says right now.

One call for the whole list rather than one per row. A sheet of two hundred
extensions would otherwise be two hundred requests against a customer's PBX
just to find out that three of them differ.
*/
func (s *Server) currentExtensions(ctx context.Context, actor identity.Actor, customer uuid.UUID) (bulk.Current, error) {
	tool, err := s.approvedTool(ctx, plugin.CapPhoneExtensions, false)
	if err != nil {
		return nil, err
	}

	raw, err := s.performTool(ctx, actor, tool, json.RawMessage(`{}`), &customer)
	if err != nil {
		return nil, err
	}

	var answer struct {
		Extensions []struct {
			Extension string `json:"extension"`
			Name      string `json:"name"`
			Enabled   bool   `json:"enabled"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, errors.New("the phone system returned a list that could not be read")
	}
	if len(answer.Extensions) == 0 {
		return nil, errors.New("the phone system reported no extensions")
	}

	now := make(bulk.Current, len(answer.Extensions))
	for _, e := range answer.Extensions {
		now[e.Extension] = bulk.Values{Name: e.Name, Enabled: e.Enabled}
	}
	return now, nil
}

/*
applyRow makes one extension's changes.

The display name is split into a first and last name, because that is the shape
the write takes and a single name is the shape a sheet has. Everything before
the first space is the first name; the rest is the last. A one-word name
becomes a first name with no last, which is what a phone system does with
"Reception" anyway.
*/
func (s *Server) applyRow(ctx context.Context, actor identity.Actor, customer uuid.UUID, row bulk.Row) error {
	tool, err := s.approvedTool(ctx, plugin.CapPhoneExtensionWrite, true)
	if err != nil {
		return err
	}

	args := map[string]any{"extension": row.Extension}
	for _, change := range row.Changes {
		switch change.Field {
		case bulk.FieldName:
			first, last, _ := strings.Cut(strings.TrimSpace(change.After), " ")
			args["first_name"] = first
			args["last_name"] = strings.TrimSpace(last)
		case bulk.FieldEnabled:
			args["enabled"] = change.After == "yes"
		}
	}

	encoded, err := json.Marshal(args)
	if err != nil {
		return err
	}
	if _, err := s.performTool(ctx, actor, tool, encoded, &customer); err != nil {
		return err
	}
	return nil
}

// approvedTool finds an approved, available provider of a capability.
func (s *Server) approvedTool(ctx context.Context, capability plugin.Capability, writes bool) (registry.Tool, error) {
	approved, err := s.DB.ApprovedTools(ctx)
	if err != nil {
		return registry.Tool{}, err
	}

	providers := s.Reg.Providers(capability)
	if writes {
		// A way of reading a thing can never return a way of writing it: the
		// two indexes are separate on purpose.
		providers = s.Reg.WriteProviders(capability)
	}
	for _, qualified := range providers {
		if _, ok := approved[qualified]; !ok {
			continue
		}
		pluginName, toolName, ok := strings.Cut(qualified, ".")
		if !ok {
			continue
		}
		if tool, found := s.Reg.Lookup(pluginName, toolName); found && tool.Available {
			return tool, nil
		}
	}
	return registry.Tool{}, fmt.Errorf("no approved tool provides %s", capability)
}

/*
createRow makes one new extension.

Named one at a time rather than handed to the plugin's own run-of-extensions
mode, because a sheet's new rows are not a run: they are whichever numbers were
free, each with its own name. Reporting them one by one is also what lets a
half-finished batch say exactly which numbers exist now.
*/
func (s *Server) createRow(ctx context.Context, actor identity.Actor, customer uuid.UUID, row bulk.Row) error {
	tool, err := s.approvedTool(ctx, plugin.CapPhoneExtensionCreate, true)
	if err != nil {
		return err
	}

	first, last, _ := strings.Cut(strings.TrimSpace(row.Wanted), " ")
	encoded, err := json.Marshal(map[string]any{
		"extensions": []map[string]any{{
			"number":     row.Extension,
			"first_name": first,
			"last_name":  strings.TrimSpace(last),
		}},
	})
	if err != nil {
		return err
	}
	if _, err := s.performTool(ctx, actor, tool, encoded, &customer); err != nil {
		return err
	}
	return nil
}

/*
revertBulk turns an applied sheet back into a sheet that undoes it.

The before values are already recorded — they are what somebody approved — so
undoing is a sheet whose after is the old before. What matters is that it goes
back through the same comparison rather than being applied straight: if a
technician has changed one of those extensions since, the revert shows that as
a change it would make, and they can untick that row instead of quietly
overwriting somebody's work.

Extensions this created are removed, because "did not exist" is what they were
before and half an undo is worse than none. Their rows are built from what the
phone system says now, not from what the sheet made, so one that has since been
named and set up reads as a removal of that — visible, and declinable, next to
everything else.

Only the rows that actually went through. A row that failed changed nothing and
has nothing to put back.
*/
func (s *Server) revertBulk(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	edit, ok := s.bulkEdit(w, r)
	if !ok {
		return
	}
	if edit.Status != "applied" {
		writeJSON(w, http.StatusConflict, errBody("only a sheet that was applied can be put back"))
		return
	}

	var plan bulk.Plan
	if err := json.Unmarshal(edit.Plan, &plan); err != nil {
		s.fail(w, err, "that plan could not be read back")
		return
	}
	var outcome []applied
	_ = json.Unmarshal(edit.Outcome, &outcome)
	went, made := map[string]bool{}, map[string]bool{}
	for _, o := range outcome {
		switch {
		case !o.OK:
		case o.New:
			made[o.Extension] = true
		case o.Gone:
			// Undoing a deletion would mean recreating it, and what came back
			// would be a number with a name on it rather than the extension
			// that was there. Not offered.
		default:
			went[o.Extension] = true
		}
	}

	// The undo, as a sheet: the values these extensions had before.
	undo := bulk.Sheet{Columns: bulk.SheetColumns()}
	for _, row := range plan.Rows {
		if !row.Changed() || !went[row.Extension] {
			continue
		}
		was := bulk.Values{}
		for _, change := range row.Changes {
			switch change.Field {
			case bulk.FieldName:
				was.Name = change.Before
			case bulk.FieldEnabled:
				was.Enabled = change.Before == "yes"
			}
		}
		// A field this sheet never touched is left out of the undo too, so
		// putting a rename back does not also assert what "Enabled" was.
		undo.Rows = append(undo.Rows, blankUntouched(bulk.Line(row.Extension, was), row))
	}

	encoded, err := json.Marshal(undo)
	if err != nil {
		s.fail(w, err, "could not build the undo")
		return
	}
	saved, err := s.DB.AddBulkEdit(r.Context(), store.BulkEdit{
		CustomerID: edit.CustomerID,
		Filename:   "undo of " + edit.Filename,
		UploadedBy: actor.Email,
		Sheet:      encoded,
	})
	if err != nil {
		s.fail(w, err, "could not store the undo")
		return
	}

	// Compared straight away, because an undo nobody has looked at is not
	// something to hand somebody an Apply button for.
	now, err := s.currentExtensions(r.Context(), actor, edit.CustomerID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(
			"could not read the phone system, so there is nothing to compare against: "+err.Error()))
		return
	}
	mapping := bulk.CanonicalMapping()
	undoPlan, err := bulk.Build(undo, mapping, now)
	if err != nil {
		s.fail(w, err, "could not work out the undo")
		return
	}

	// What this sheet created, as it stands today.
	for _, row := range plan.Rows {
		if !row.Creates() || !made[row.Extension] {
			continue
		}
		state, still := now[row.Extension]
		if !still {
			// Already gone. Nothing to put back, and saying so would be a row
			// that does nothing.
			continue
		}
		undoPlan.Rows = append(undoPlan.Rows, bulk.Row{
			Extension: row.Extension,
			Name:      state.Name,
			Gone:      true,
		})
		undoPlan.Removing++
	}
	undoPlan.Sort()

	if undoPlan.Changing+undoPlan.Removing == 0 {
		writeJSON(w, http.StatusBadRequest, errBody(
			"there is nothing to put back — either nothing in that sheet went through, or it has already been undone"))
		return
	}

	encodedMapping, _ := json.Marshal(mapping)
	encodedPlan, err := json.Marshal(undoPlan)
	if err != nil {
		s.fail(w, err, "could not store the undo")
		return
	}
	planned, err := s.DB.SaveBulkPlan(r.Context(), saved.ID, encodedMapping, encodedPlan)
	if err != nil {
		s.fail(w, err, "could not store the undo")
		return
	}

	customerID := edit.CustomerID
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "bulk.revert",
		Outcome:     audit.OutcomeOK,
		CustomerID:  &customerID,
		Detail: fmt.Sprintf("%s: %d to put back, %d to remove",
			edit.Filename, undoPlan.Changing, undoPlan.Removing),
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"edit": planned, "plan": undoPlan,
	})
}

/*
blankUntouched empties the cells for fields the original sheet did not change.

An undo asserts only what it is undoing. Writing every column would mean
putting a rename back also states what Enabled was at the time — true when the
plan was made, and quite possibly not now.
*/
func blankUntouched(line []string, row bulk.Row) []string {
	touched := map[bulk.Field]bool{}
	for _, change := range row.Changes {
		touched[change.Field] = true
	}
	mapping := bulk.CanonicalMapping()
	for field, col := range mapping.Fields {
		if !touched[field] && col < len(line) {
			line[col] = ""
		}
	}
	return line
}

/*
listExtensions hands back what the phone system says right now.

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
	now, err := s.currentExtensions(r.Context(), actor, customerID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody("could not read the phone system: "+err.Error()))
		return
	}

	type extension struct {
		Extension string `json:"extension"`
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
	}
	list := make([]extension, 0, len(now))
	for _, number := range inOrder(now) {
		list = append(list, extension{number, now[number].Name, now[number].Enabled})
	}
	// The column names go with the list so the console can show what a sheet
	// has to look like without keeping its own copy of the answer.
	writeJSON(w, http.StatusOK, map[string]any{
		"extensions": list,
		"columns":    bulk.SheetColumns(),
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
	now, err := s.currentExtensions(r.Context(), actor, customerID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody("could not read the phone system: "+err.Error()))
		return
	}

	var out strings.Builder
	sheet := csv.NewWriter(&out)
	_ = sheet.Write(bulk.SheetColumns())
	for _, number := range inOrder(now) {
		_ = sheet.Write(bulk.Line(number, now[number]))
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

// chosenRow is one extension somebody ticked in the console, with what they
// want it to say.
type chosenRow struct {
	Extension string `json:"extension"`
	Name      string `json:"name"`
	Enabled   string `json:"enabled"`
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
		Rows []chosenRow `json:"rows"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	if len(body.Rows) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody("choose at least one extension"))
		return
	}
	if len(body.Rows) > bulk.MaxRows {
		writeJSON(w, http.StatusBadRequest, errBody("that is more extensions than this will take at once"))
		return
	}

	sheet := bulk.Sheet{Columns: bulk.SheetColumns()}
	for _, row := range body.Rows {
		sheet.Rows = append(sheet.Rows, []string{
			strings.TrimSpace(row.Extension),
			strings.TrimSpace(row.Name),
			strings.TrimSpace(row.Enabled),
		})
	}

	now, err := s.currentExtensions(r.Context(), actor, customerID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(
			"could not read the phone system, so there is nothing to compare against: "+err.Error()))
		return
	}
	mapping := bulk.CanonicalMapping()
	plan, err := bulk.Build(sheet, mapping, now)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
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
		Detail:      fmt.Sprintf("%d chosen, %d would change", len(body.Rows), plan.Changing),
	})

	writeJSON(w, http.StatusOK, map[string]any{"edit": planned, "plan": plan})
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
	if _, err := s.performTool(ctx, actor, tool, encoded, &customer); err != nil {
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
