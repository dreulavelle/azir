package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	Problem   string `json:"problem,omitempty"`
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

	results := make([]applied, 0, plan.Changing)
	var done, failed int
	for _, row := range plan.Rows {
		if !row.Changed() {
			continue
		}
		if err := s.applyRow(r.Context(), actor, edit.CustomerID, row); err != nil {
			results = append(results, applied{Extension: row.Extension, Problem: err.Error()})
			failed++
			continue
		}
		results = append(results, applied{Extension: row.Extension, OK: true})
		done++
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
		Detail:      fmt.Sprintf("%s: %d changed, %d failed", edit.Filename, done, failed),
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"edit": saved, "results": results, "changed": done, "failed": failed,
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
