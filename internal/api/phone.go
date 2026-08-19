package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/blf"
	"github.com/dreulavelle/azir/internal/bulk"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/registry"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/pkg/plugin"
)

/*
Reading a phone system, and writing to it.

Everything that talks to a customer's phone system on behalf of the screens
that change it: one read that answers what every extension is set to, and the
handful of writes that put something back. The spreadsheet flow next door and
the form flow beside it both go through here, which is the point — two ways in
and one way out, so an approval, an audit line and a batch size mean the same
thing whichever screen somebody used.
*/

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
	got, err := s.phoneNow(ctx, actor, customer)
	return got.Now, got.Specs, err
}

/*
phoneState is everything one read of a phone system tells Azir.

A struct rather than a handful of return values, because the read grew: what
every extension is set to, what the phone system will let anybody change, the
key layout on each desk phone, and whether the list is all of them. Four things
that arrive together and are useful together.
*/
type phoneState struct {
	Now   bulk.Current
	Specs []bulk.Spec
	// Keys is each extension's phone buttons, by extension number.
	Keys map[string][]blf.Key
	// Complete is false when the phone system had more extensions than it
	// would hand over in one sweep.
	Complete bool
}

/*
currentWithCompleteness is currentExtensions, plus whether the phone system had
more extensions than it was willing to hand over in one sweep.

Worth carrying separately because it changes what a screen may claim. A list
that quietly ends is read as the whole list, and somebody changing "all of
them" from a truncated one would miss whatever was past the cut.
*/
func (s *Server) currentWithCompleteness(ctx context.Context, actor identity.Actor, customer uuid.UUID) (bulk.Current, []bulk.Spec, bool, error) {
	got, err := s.phoneNow(ctx, actor, customer)
	return got.Now, got.Specs, got.Complete, err
}

/*
phoneNow reads a phone system, falling back to the plain extension list when
the settings capability is not available.

The fallback is worth having rather than refusing: a deployment that has not
approved the settings tool can still rename forty extensions, it just cannot
see or change the other thirty fields.
*/
func (s *Server) phoneNow(ctx context.Context, actor identity.Actor, customer uuid.UUID) (phoneState, error) {
	if got, err := s.readSettings(ctx, actor, customer); err == nil {
		return got, nil
	}
	now, specs, err := s.currentExtensions(ctx, actor, customer)
	return phoneState{Now: now, Specs: specs, Keys: map[string][]blf.Key{}, Complete: true}, err
}

func (s *Server) readSettings(ctx context.Context, actor identity.Actor, customer uuid.UUID) (phoneState, error) {
	tool, err := s.approvedTool(ctx, plugin.CapPhoneExtensionSettings, false)
	if err != nil {
		return phoneState{}, err
	}
	raw, err := s.readTool(ctx, actor, tool, json.RawMessage(`{}`), &customer)
	if err != nil {
		return phoneState{}, err
	}

	var answer struct {
		Extensions []struct {
			Extension string         `json:"extension"`
			Name      string         `json:"name"`
			Settings  map[string]any `json:"settings"`
			Keys      []blf.Key      `json:"keys"`
		} `json:"extensions"`
		Fields   []bulk.Spec `json:"fields"`
		Complete *bool       `json:"complete"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return phoneState{}, errors.New("the phone system returned settings that could not be read")
	}
	if len(answer.Extensions) == 0 {
		return phoneState{}, errors.New("the phone system reported no extensions")
	}
	// A plugin that says nothing about completeness is taken at its word.
	complete := answer.Complete == nil || *answer.Complete

	specs := bulk.Merge(bulk.Core, answer.Fields)
	byField := make(map[bulk.Field]bulk.Spec, len(specs))
	for _, spec := range specs {
		byField[spec.Field] = spec
	}

	now := make(bulk.Current, len(answer.Extensions))
	keys := make(map[string][]blf.Key, len(answer.Extensions))
	for _, e := range answer.Extensions {
		keys[e.Extension] = e.Keys
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
	return phoneState{Now: now, Specs: specs, Keys: keys, Complete: complete}, nil
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
