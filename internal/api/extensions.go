package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/bulk"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/pkg/plugin"
)

/*
One extension at a time.

The sheet path stages a change, compares it and asks somebody to approve the
difference, which is the right shape for forty extensions and the wrong one for
renaming a person. This is the other shape: a form on one extension, applied
when they press save, with what changed already on the screen in front of them.

Same gates either way. Every write goes through the same capability, the same
approval, the same permission and the same audit line — the difference is only
how many extensions it touches and whether a diff was worth a separate screen.

Guarded by phone.manage throughout.
*/

// wanted is what a form says an extension should be, field by field.
type wanted struct {
	Number string            `json:"number"`
	Values map[string]string `json:"values"`
}

/*
setExtension applies a form's changes to one extension.

Split by capability rather than sent as one object: what an extension is called
and whether it is on go through the extension-change capability, and everything
the phone system publishes goes through the options one. Two powers, approved
separately, and a form that used one to do the other's job would quietly widen
what an approval meant.
*/
func (s *Server) setExtension(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, ok := s.customerOf(w, r)
	if !ok {
		return
	}
	number := strings.TrimSpace(r.PathValue("number"))
	if number == "" {
		writeJSON(w, http.StatusBadRequest, errBody("say which extension"))
		return
	}

	var body wanted
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	if len(body.Values) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody("nothing to change"))
		return
	}

	specs := s.specsFor(r.Context(), actor, customerID)
	core, settings, err := sortOut(body.Values, specs)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}

	if len(core) > 0 {
		if err := s.applyRow(r.Context(), actor, customerID, number, core); err != nil {
			writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
			return
		}
	}
	if len(settings) > 0 {
		said, err := s.setOptions(r.Context(), actor, customerID, []string{number}, settings)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
			return
		}
		if why := said[number]; why != "" {
			writeJSON(w, http.StatusBadGateway, errBody(why))
			return
		}
	}

	// The names of what changed, never the values: a voicemail PIN is in here.
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "extension.change",
		Outcome:     audit.OutcomeOK,
		CustomerID:  &customerID,
		Detail:      fmt.Sprintf("extension %s: %s", number, named(body.Values)),
	})

	writeJSON(w, http.StatusOK, map[string]any{"extension": number, "changed": len(body.Values)})
}

/*
createExtension makes one and then sets everything else on it.

Two calls, because creating and configuring are two capabilities and the create
one takes a number and a name. Reported as created either way: an extension
that exists with half its settings is a different problem from one that was
never made, and saying "failed" would send somebody looking for the wrong one.
*/
func (s *Server) createExtension(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, ok := s.customerOf(w, r)
	if !ok {
		return
	}

	var body wanted
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	number := strings.TrimSpace(body.Number)
	if number == "" {
		writeJSON(w, http.StatusBadRequest, errBody("a new extension needs a number"))
		return
	}
	first := strings.TrimSpace(body.Values["FirstName"])
	last := strings.TrimSpace(body.Values["LastName"])
	if first == "" && last == "" {
		writeJSON(w, http.StatusBadRequest, errBody("a new extension needs a name"))
		return
	}

	tool, err := s.approvedTool(r.Context(), plugin.CapPhoneExtensionCreate, true)
	if err != nil {
		writeJSON(w, http.StatusForbidden, errBody(err.Error()))
		return
	}
	encoded, err := json.Marshal(map[string]any{
		"extensions": []map[string]any{{
			"number": number, "first_name": first, "last_name": last,
		}},
	})
	if err != nil {
		s.fail(w, err, "could not ask for that extension")
		return
	}
	if _, err := s.performTool(r.Context(), actor, tool, encoded, &customerID, byHand); err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
		return
	}

	// Made. Everything from here is configuration on an extension that exists.
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "extension.create",
		Outcome:     audit.OutcomeOK,
		CustomerID:  &customerID,
		Detail:      fmt.Sprintf("extension %s", number),
	})

	rest := map[string]string{}
	for field, value := range body.Values {
		if field == "FirstName" || field == "LastName" {
			continue
		}
		rest[field] = value
	}
	if len(rest) == 0 {
		writeJSON(w, http.StatusCreated, map[string]any{"extension": number})
		return
	}

	specs := s.specsFor(r.Context(), actor, customerID)
	core, settings, err := sortOut(rest, specs)
	if err != nil {
		writeJSON(w, http.StatusCreated, map[string]any{
			"extension": number, "settings_problem": err.Error()})
		return
	}
	problems := []string{}
	if len(core) > 0 {
		if err := s.applyRow(r.Context(), actor, customerID, number, core); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(settings) > 0 {
		said, err := s.setOptions(r.Context(), actor, customerID, []string{number}, settings)
		switch {
		case err != nil:
			problems = append(problems, err.Error())
		case said[number] != "":
			problems = append(problems, said[number])
		}
	}
	if len(problems) > 0 {
		writeJSON(w, http.StatusCreated, map[string]any{
			"extension": number, "settings_problem": strings.Join(problems, "; ")})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"extension": number})
}

// removeExtensions deletes the ones named, and only the ones named.
func (s *Server) removeExtensions(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, ok := s.customerOf(w, r)
	if !ok {
		return
	}
	var body struct {
		Extensions []string `json:"extensions"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	if len(body.Extensions) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody("say which extensions"))
		return
	}

	tool, err := s.approvedTool(r.Context(), plugin.CapPhoneExtensionDelete, true)
	if err != nil {
		writeJSON(w, http.StatusForbidden, errBody(err.Error()))
		return
	}

	// The same fifty at a time the options path uses, and for the same reason:
	// the plugin will not take "every extension" in one call, and Azir is
	// naming them explicitly. Removing a hundred and twenty should not fail
	// because it is more than fifty.
	removed := 0
	for _, batch := range inBatches(body.Extensions, atOnce) {
		encoded, err := json.Marshal(map[string]any{"extensions": batch})
		if err != nil {
			s.fail(w, err, "could not ask for that")
			return
		}
		if _, err := s.performTool(r.Context(), actor, tool, encoded, &customerID, byHand); err != nil {
			// Whatever went before this is gone. Reporting a clean failure
			// would send somebody looking for extensions that no longer exist.
			s.Audit.Record(r.Context(), audit.Event{
				ActorUserID: actor.Email,
				Action:      "extension.remove",
				Outcome:     audit.OutcomeFailed,
				CustomerID:  &customerID,
				Detail:      fmt.Sprintf("%d removed before this failed", removed),
			})
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": err.Error(), "removed": removed,
			})
			return
		}
		removed += len(batch)
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "extension.remove",
		Outcome:     audit.OutcomeOK,
		CustomerID:  &customerID,
		Detail:      strings.Join(body.Extensions, ", "),
	})
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

/*
sortOut splits a form's values by the capability that writes them, checking
each against what the phone system says it accepts.

Refused here rather than at the phone system, so a bad choice is a sentence
naming the field instead of a 400 from a PBX halfway through.
*/
func sortOut(values map[string]string, specs []bulk.Spec) (core []bulk.Change, settings map[string]any, err error) {
	byField := make(map[bulk.Field]bulk.Spec, len(specs))
	for _, spec := range specs {
		byField[spec.Field] = spec
	}

	// Setting the display name and its parts in one request is a contradiction:
	// the phone system builds one from the other, and which wins would come
	// down to the order they happened to be sent in.
	if _, whole := values[string(bulk.FieldName)]; whole && bulk.Splits(specs) {
		_, first := values["FirstName"]
		_, last := values["LastName"]
		if first || last {
			return nil, nil, fmt.Errorf(
				"set the first and last name, or the display name, but not both — the phone system builds one from the other")
		}
	}

	settings = map[string]any{}
	// Sorted, so the same form makes the same request every time — which keeps
	// it readable in a log and comparable between two runs.
	fields := make([]string, 0, len(values))
	for field := range values {
		fields = append(fields, field)
	}
	sort.Strings(fields)

	for _, field := range fields {
		spec, known := byField[bulk.Field(field)]
		if !known {
			return nil, nil, fmt.Errorf("this phone system has no setting called %q", field)
		}
		raw := values[field]

		// A secret goes as it was typed and is never normalised, compared or
		// logged. Empty means leave whatever is there.
		if spec.Kind == bulk.KindSecret {
			if strings.TrimSpace(raw) != "" {
				settings[field] = raw
			}
			continue
		}

		after, err := bulk.Normalise(spec, raw)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", spec.Label, err)
		}
		switch spec.Field {
		case bulk.FieldName, bulk.FieldEnabled:
			core = append(core, bulk.Change{
				Field: spec.Field, Label: spec.Label, After: after,
			})
		default:
			if spec.Kind == bulk.KindBool {
				settings[field] = after == "yes"
				continue
			}
			settings[field] = after
		}
	}
	return core, settings, nil
}

/*
named lists which fields were set, and never what they were set to.

The activity log says what somebody changed about an extension. What they
changed it to belongs on the extension, not in a log that is kept for weeks —
and one of these fields is a voicemail PIN.
*/
func named(values map[string]string) string {
	fields := make([]string, 0, len(values))
	for field := range values {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return strings.Join(fields, ", ")
}

// customerOf reads and checks the customer a request is about.
func (s *Server) customerOf(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	customerID, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("customer_id")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("say which customer this is for"))
		return uuid.UUID{}, false
	}
	if _, err := s.DB.GetCustomer(r.Context(), customerID); err != nil {
		writeJSON(w, http.StatusNotFound, errBody("no such customer"))
		return uuid.UUID{}, false
	}
	return customerID, true
}

/*
listExtensionsPage answers the table: a page of it, searched, with a count.

A thousand-extension deployment is 1.7MB of JSON and a thousand rows of DOM if
the whole thing is sent, on every navigation, for a screen that shows twenty of
them at a time. The search and the paging happen here so the browser is handed
what it draws and nothing else.

Slim rows on purpose. The table shows a number, a name, an email and a handful
of marks; the other twenty-nine fields are what the editor asks for about one
extension when somebody opens it.
*/
func (s *Server) listExtensionsPage(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, ok := s.customerOf(w, r)
	if !ok {
		return
	}
	now, specs, complete, err := s.currentWithCompleteness(r.Context(), actor, customerID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody("could not read the phone system: "+err.Error()))
		return
	}

	find := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	limit, offset := paging(r)

	// Matched here rather than in the browser, because the browser is not
	// going to be holding the rows that do not match.
	numbers := inOrder(now)
	matched := make([]string, 0, len(numbers))
	for _, number := range numbers {
		if find == "" ||
			strings.Contains(number, find) ||
			strings.Contains(strings.ToLower(now[number][bulk.FieldName]), find) ||
			strings.Contains(strings.ToLower(now[number]["EmailAddress"]), find) {
			matched = append(matched, number)
		}
	}

	page := matched
	if offset < len(page) {
		page = page[offset:]
	} else {
		page = nil
	}
	if len(page) > limit {
		page = page[:limit]
	}

	// What the table draws, and nothing else.
	type row struct {
		Extension string `json:"extension"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		Enabled   bool   `json:"enabled"`
		Recording bool   `json:"recording"`
		Voicemail bool   `json:"voicemail"`
		Tunnel    bool   `json:"tunnel_blocked"`
		NoAudio   bool   `json:"no_audio"`
	}
	rows := make([]row, 0, len(page))
	for _, number := range page {
		v := now[number]
		rows = append(rows, row{
			Extension: number,
			Name:      v[bulk.FieldName],
			Email:     v["EmailAddress"],
			Enabled:   v[bulk.FieldEnabled] != "no",
			Recording: v["RecordCalls"] == "yes",
			Voicemail: v["VMEnabled"] == "yes",
			Tunnel:    v["BlockTunnel"] == "yes",
			NoAudio:   v["PbxDeliversAudio"] == "no",
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"extensions": rows,
		"total":      len(matched),
		"all":        len(numbers),
		"fields":     specs,
		"complete":   complete,
		"offset":     offset,
	})
}

/*
extensionValues answers the editor: everything about the extensions named.

One when somebody opens a row, several when they edit a selection together —
and never the whole system, which is what the table is for. Named explicitly
so the size of the answer is the size of the question.
*/
func (s *Server) extensionValues(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, ok := s.customerOf(w, r)
	if !ok {
		return
	}
	asked := strings.Split(r.URL.Query().Get("extensions"), ",")
	wantOne := strings.TrimSpace(r.PathValue("number"))
	if wantOne != "" {
		asked = []string{wantOne}
	}

	names := make([]string, 0, len(asked))
	for _, number := range asked {
		if trimmed := strings.TrimSpace(number); trimmed != "" {
			names = append(names, trimmed)
		}
	}
	if len(names) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody("say which extensions"))
		return
	}
	if len(names) > bulk.MaxRows {
		writeJSON(w, http.StatusBadRequest, errBody("that is more extensions than this will read at once"))
		return
	}

	now, specs, _, err := s.currentWithCompleteness(r.Context(), actor, customerID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody("could not read the phone system: "+err.Error()))
		return
	}

	type one struct {
		Extension string            `json:"extension"`
		Name      string            `json:"name"`
		Enabled   bool              `json:"enabled"`
		Values    map[string]string `json:"values"`
	}
	out := make([]one, 0, len(names))
	missing := []string{}
	for _, number := range names {
		v, known := now[number]
		if !known {
			missing = append(missing, number)
			continue
		}
		values := make(map[string]string, len(v))
		for field, text := range v {
			values[string(field)] = text
		}
		out = append(out, one{
			Extension: number,
			Name:      v[bulk.FieldName],
			Enabled:   v[bulk.FieldEnabled] != "no",
			Values:    values,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"extensions": out, "fields": specs, "missing": missing,
	})
}

// paging reads how much of a list to send, with a ceiling so a caller cannot
// ask for a thousand rows by leaving the parameter off.
func paging(r *http.Request) (limit, offset int) {
	limit, offset = 50, 0
	if asked, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && asked > 0 {
		limit = asked
	}
	if limit > 200 {
		limit = 200
	}
	if asked, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && asked > 0 {
		offset = asked
	}
	return limit, offset
}
