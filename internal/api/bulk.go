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
currentExtensions reads what the phone system says right now, and what it will
let somebody change.

One call for the whole list rather than one per row. A sheet of two hundred
extensions would otherwise be two hundred requests against a customer's PBX
just to find out that three of them differ.

The settings capability is asked first because it answers both questions at
once: every extension's editable options, and the list of what those options
are. Without it — not approved, not offered — this falls back to the plain
extension list, and the sheet carries the two fields Azir knows about on its
own. That is worth having rather than refusing: a deployment that has not
approved the settings tool can still rename forty extensions.
*/
func (s *Server) currentExtensions(ctx context.Context, actor identity.Actor, customer uuid.UUID) (bulk.Current, []bulk.Spec, error) {
	if now, specs, err := s.extensionSettings(ctx, actor, customer); err == nil {
		return now, specs, nil
	}

	tool, err := s.approvedTool(ctx, plugin.CapPhoneExtensions, false)
	if err != nil {
		return nil, nil, err
	}
	raw, err := s.readTool(ctx, actor, tool, json.RawMessage(`{}`), &customer)
	if err != nil {
		return nil, nil, err
	}

	var answer struct {
		Extensions []struct {
			Extension string `json:"extension"`
			Name      string `json:"name"`
			Enabled   bool   `json:"enabled"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, nil, errors.New("the phone system returned a list that could not be read")
	}
	if len(answer.Extensions) == 0 {
		return nil, nil, errors.New("the phone system reported no extensions")
	}

	now := make(bulk.Current, len(answer.Extensions))
	for _, e := range answer.Extensions {
		now[e.Extension] = bulk.Values{
			bulk.FieldName:    e.Name,
			bulk.FieldEnabled: yesNo(e.Enabled),
		}
	}
	return now, bulk.Core, nil
}

/*
extensionSettings reads every extension's options, and the list of what they
are.

The field list travels with the values on purpose. Azir does not know what a
3CX extension can be set to and should not: the plugin owns that, publishes it,
and the columns of the sheet, the choices in the form and the allowlist on the
way back out are all built from the one list.
*/
func (s *Server) extensionSettings(ctx context.Context, actor identity.Actor, customer uuid.UUID) (bulk.Current, []bulk.Spec, error) {
	now, specs, _, err := s.extensionSettingsFull(ctx, actor, customer)
	return now, specs, err
}

/*
currentWithCompleteness is currentExtensions, plus whether the phone system had
more extensions than it was willing to hand over in one sweep.

Worth carrying separately because it changes what a screen may claim. A list
that quietly ends is read as the whole list, and somebody changing "all of
them" from a truncated one would miss whatever was past the cut.
*/
func (s *Server) currentWithCompleteness(ctx context.Context, actor identity.Actor, customer uuid.UUID) (bulk.Current, []bulk.Spec, bool, error) {
	if now, specs, complete, err := s.extensionSettingsFull(ctx, actor, customer); err == nil {
		return now, specs, complete, nil
	}
	now, specs, err := s.currentExtensions(ctx, actor, customer)
	return now, specs, true, err
}

func (s *Server) extensionSettingsFull(ctx context.Context, actor identity.Actor, customer uuid.UUID) (bulk.Current, []bulk.Spec, bool, error) {
	tool, err := s.approvedTool(ctx, plugin.CapPhoneExtensionSettings, false)
	if err != nil {
		return nil, nil, false, err
	}
	raw, err := s.readTool(ctx, actor, tool, json.RawMessage(`{}`), &customer)
	if err != nil {
		return nil, nil, false, err
	}

	var answer struct {
		Extensions []struct {
			Extension string         `json:"extension"`
			Name      string         `json:"name"`
			Settings  map[string]any `json:"settings"`
		} `json:"extensions"`
		Fields   []bulk.Spec `json:"fields"`
		Complete *bool       `json:"complete"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, nil, false, errors.New("the phone system returned settings that could not be read")
	}
	if len(answer.Extensions) == 0 {
		return nil, nil, false, errors.New("the phone system reported no extensions")
	}
	// A plugin that says nothing about completeness is taken at its word.
	complete := answer.Complete == nil || *answer.Complete

	specs := bulk.Merge(bulk.Core, answer.Fields)
	byField := make(map[bulk.Field]bulk.Spec, len(specs))
	for _, spec := range specs {
		byField[spec.Field] = spec
	}

	now := make(bulk.Current, len(answer.Extensions))
	for _, e := range answer.Extensions {
		values := bulk.Values{bulk.FieldName: e.Name}
		for name, value := range e.Settings {
			spec, wanted := byField[bulk.Field(name)]
			if !wanted {
				continue
			}
			// Normalised on the way in, so a phone system saying true and a
			// spreadsheet saying "Yes" are one answer rather than a change.
			text, err := bulk.Normalise(spec, asText(value))
			if err != nil {
				continue
			}
			values[spec.Field] = text
		}
		// 3CX publishes its own Enabled, which Merge drops in favour of
		// Azir's. The value still has to land somewhere.
		if _, ok := e.Settings["Enabled"]; ok {
			values[bulk.FieldEnabled] = asText(e.Settings["Enabled"])
			if on, err := readTruth(e.Settings["Enabled"]); err == nil {
				values[bulk.FieldEnabled] = yesNo(on)
			}
		}
		now[e.Extension] = values
	}
	return now, specs, complete, nil
}

/*
specsFor is what this customer's phone system will let a sheet change.

Never fails. A page that cannot reach the PBX still has to render, and Azir's
own two fields are a worse answer than thirty-two but a much better one than an
error where the column list should be.
*/
func (s *Server) specsFor(ctx context.Context, actor identity.Actor, customer uuid.UUID) []bulk.Spec {
	_, specs, err := s.extensionSettings(ctx, actor, customer)
	if err != nil {
		return bulk.Core
	}
	return specs
}

// asText reads whatever JSON shape a setting arrived in as a string.
func asText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return yesNo(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

func readTruth(v any) (bool, error) {
	if b, ok := v.(bool); ok {
		return b, nil
	}
	return false, errors.New("not a yes or a no")
}

func yesNo(on bool) string {
	if on {
		return "yes"
	}
	return "no"
}

/*
applyRow makes one extension's core changes.

Only the two Azir knows on its own — what it is called, and whether it is on.
Everything the phone system publishes goes through setOptions instead, which is
the capability that owns it and can take many extensions at once.

The display name is split into a first and last name, because that is the shape
the write takes and a single name is the shape a sheet has. Everything before
the first space is the first name; the rest is the last. A one-word name
becomes a first name with no last, which is what a phone system does with
"Reception" anyway.
*/
func (s *Server) applyRow(ctx context.Context, actor identity.Actor, customer uuid.UUID, extension string, changes []bulk.Change) error {
	tool, err := s.approvedTool(ctx, plugin.CapPhoneExtensionWrite, true)
	if err != nil {
		return err
	}

	args := map[string]any{"extension": extension}
	for _, change := range changes {
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
	if _, err := s.performTool(ctx, actor, tool, encoded, &customer, fromSheet); err != nil {
		return err
	}
	return nil
}

/*
setOptions applies one set of options to many extensions in one request.

Returns what went wrong per extension, keyed by number, so a batch where two of
forty were refused says which two. An empty entry is a success; a missing one
means the phone system did not mention it, which is reported rather than
assumed either way.
*/
func (s *Server) setOptions(ctx context.Context, actor identity.Actor, customer uuid.UUID, extensions []string, settings map[string]any) (map[string]string, error) {
	tool, err := s.approvedTool(ctx, plugin.CapPhoneExtensionOptions, true)
	if err != nil {
		return nil, err
	}

	said := make(map[string]string, len(extensions))
	for _, batch := range inBatches(extensions, atOnce) {
		encoded, err := json.Marshal(map[string]any{
			"extensions": batch,
			"options":    settings,
		})
		if err != nil {
			return nil, err
		}
		raw, err := s.performTool(ctx, actor, tool, encoded, &customer, fromSheet)
		if err != nil {
			// The batches before this one went through. Saying so beats
			// reporting a clean failure for a change that half happened.
			for _, extension := range batch {
				said[extension] = err.Error()
			}
			for _, rest := range extensions {
				if _, known := said[rest]; !known {
					said[rest] = "not attempted, because an earlier batch failed"
				}
			}
			return said, nil
		}

		var answer struct {
			Results []struct {
				Extension string `json:"extension"`
				Changed   bool   `json:"changed"`
				Reason    string `json:"reason"`
			} `json:"results"`
		}
		if err := json.Unmarshal(raw, &answer); err != nil {
			return nil, errors.New("the phone system answered in a way that could not be read")
		}

		mentioned := map[string]bool{}
		for _, r := range answer.Results {
			mentioned[r.Extension] = true
			if !r.Changed {
				said[r.Extension] = reasonOr(r.Reason, "the phone system did not change it")
			}
		}
		for _, extension := range batch {
			if !mentioned[extension] {
				said[extension] = "the phone system did not say what happened to it"
			}
		}
	}
	return said, nil
}

/*
atOnce is how many extensions go in one request to the phone system.

The plugin refuses more than fifty, deliberately: "every extension" should not
be something a single call can express by accident. Azir names them explicitly
from a plan somebody approved, so the guard is not protecting it from anything
— but removing the guard would remove it for everyone. Splitting here keeps
both: fifty at a time, and no ceiling on how many a change can cover.
*/
const atOnce = 50

// inBatches cuts a list into runs of at most n, keeping the order so a partly
// applied change is a prefix rather than a scatter.
func inBatches(all []string, n int) [][]string {
	var out [][]string
	for start := 0; start < len(all); start += n {
		end := start + n
		if end > len(all) {
			end = len(all)
		}
		out = append(out, all[start:end])
	}
	return out
}

func reasonOr(reason, fallback string) string {
	if strings.TrimSpace(reason) == "" {
		return fallback
	}
	return reason
}

/*
splitChanges separates what Azir writes itself from what the phone system's
own options endpoint writes.

Two capabilities, deliberately: renaming an extension and setting thirty
options on it are different powers, approved separately, and a plan that does
both should need both rather than one standing in for the other.
*/
func splitChanges(changes []bulk.Change) (core, options []bulk.Change) {
	for _, change := range changes {
		if change.Field == bulk.FieldName || change.Field == bulk.FieldEnabled {
			core = append(core, change)
			continue
		}
		options = append(options, change)
	}
	return core, options
}

// settingsFor turns approved changes back into the typed values the phone
// system takes. The text in a plan is what a person read; this is what goes
// out.
func settingsFor(changes []bulk.Change, byField map[bulk.Field]bulk.Spec) map[string]any {
	settings := make(map[string]any, len(changes))
	for _, change := range changes {
		if byField[change.Field].Kind == bulk.KindBool {
			settings[string(change.Field)] = change.After == "yes"
			continue
		}
		settings[string(change.Field)] = change.After
	}
	return settings
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
	if _, err := s.performTool(ctx, actor, tool, encoded, &customer, fromSheet); err != nil {
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

	// Read first, because the undo is compared against this and its columns
	// are whatever the phone system says it will write.
	now, specs, err := s.currentExtensions(r.Context(), actor, edit.CustomerID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(
			"could not read the phone system, so there is nothing to compare against: "+err.Error()))
		return
	}

	// The undo, as a sheet: the values these extensions had before. A field
	// this sheet never touched is simply absent, so putting a rename back does
	// not also assert what everything else was at the time.
	undo := bulk.Sheet{Columns: bulk.SheetColumns(specs)}
	for _, row := range plan.Rows {
		if !row.Changed() || !went[row.Extension] {
			continue
		}
		was := bulk.Values{}
		for _, change := range row.Changes {
			was[change.Field] = change.Before
		}
		undo.Rows = append(undo.Rows, bulk.Line(row.Extension, was, specs))
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
	mapping := bulk.CanonicalMapping(specs)
	undoPlan, err := bulk.Build(undo, mapping, now, specs)
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
			Name:      state[bulk.FieldName],
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
