package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/bulk"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/pkg/plugin"
)

/*
Importing a carrier's list of DID numbers onto a trunk.

The same three steps as the sheet of extensions next door, and for the same
reason: a file is parsed here, compared against what the phone system says right
now, and a person approves the difference. What a carrier sends is a list of
numbers they have sold you, not a description of the trunk — half of them are
usually on it already, and the ones that are not are the import.

The file never reaches the model, exactly as in bulk.go. It is read by Azir,
compared here, and shown on a screen. A customer's DID list is their data.

Smaller than the extension flow because a DID has nothing to map: one column of
numbers, optionally a second naming the extension each should ring. There is no
field mapping step because there are no fields, and no type to say beside the
extension because a 3CX numbering plan is one space — the number already says
whether it is a person, a queue or a ring group.

Nothing here is stored between the two steps. The apply is given the numbers
explicitly rather than reading back a plan, which means the only thing that can
be applied is the list a person was actually shown — no row can appear between
the preview and the confirmation because there is nothing in between to change.

Guarded by phone.manage throughout, including the reading, for the reason
bulk.go gives: somebody who cannot change one extension has no business staging
two hundred numbers.
*/

// didTrunk is one trunk as the picker needs it.
type didTrunk struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	Number    string            `json:"number"`
	Host      string            `json:"host"`
	Direction string            `json:"direction"`
	Online    bool              `json:"online"`
	DIDs      []string          `json:"dids"`
	Routes    map[string]string `json:"routes"`
}

// listDIDTrunks answers the picker: which trunks this customer's phone system
// has, and what each already carries.
func (s *Server) listDIDTrunks(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, ok := s.didCustomer(w, r)
	if !ok {
		return
	}

	trunks, err := s.currentTrunks(r.Context(), actor, customerID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(
			"could not read the phone system's trunks: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trunks": trunks})
}

// currentTrunks reads the trunks through the capability, so this screen never
// learns which plugin is behind it.
func (s *Server) currentTrunks(ctx context.Context, actor identity.Actor, customer uuid.UUID) ([]didTrunk, error) {
	tool, err := s.approvedTool(ctx, plugin.CapPhoneTrunks, false)
	if err != nil {
		return nil, err
	}
	raw, err := s.readTool(ctx, actor, tool, json.RawMessage(`{}`), &customer)
	if err != nil {
		return nil, err
	}

	var answer struct {
		Trunks []didTrunk `json:"trunks"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, fmt.Errorf("the phone system returned a trunk list that could not be read")
	}
	return answer.Trunks, nil
}

/*
previewDIDs compares an uploaded list against the trunk as it is now.

Every number comes back in exactly one bucket, because a screen that shows a
number twice under two headings is one nobody can count from. The buckets are
what a person needs to decide with:

  - adding: not on this trunk, and will be
  - already: on this trunk already, so nothing to do
  - elsewhere: on a different trunk, which is usually a mistake somebody wants
    to know about before they make it a second one
  - routed: already assigned to a different extension, so what happens to it
    depends on whether the import appends or replaces
  - refused: could not be read at all
*/
func (s *Server) previewDIDs(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, ok := s.didCustomer(w, r)
	if !ok {
		return
	}

	if err := r.ParseMultipartForm(bulk.MaxBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("that upload could not be read"))
		return
	}
	file, header, err := r.FormFile("sheet")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("attach the list as 'sheet'"))
		return
	}
	defer file.Close() //nolint:errcheck // read-only

	parsed, err := bulk.ParseDIDFile(file)
	if err != nil {
		// Written for the person who chose the file.
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	if len(parsed.Numbers) == 0 && len(parsed.Refused) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody("there are no numbers in that file"))
		return
	}

	trunks, err := s.currentTrunks(r.Context(), actor, customerID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(
			"could not read the phone system, so there is nothing to compare against: "+err.Error()))
		return
	}

	wanted := strings.TrimSpace(r.URL.Query().Get("trunk"))
	on, found := pickTrunk(trunks, wanted)
	if !found {
		writeJSON(w, http.StatusNotFound, errBody("this phone system has no such trunk"))
		return
	}

	preview := comparePreview(parsed, on, trunks)
	preview["trunk"] = on
	preview["filename"] = header.Filename

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "dids.preview",
		Outcome:     audit.OutcomeOK,
		CustomerID:  &customerID,
		Detail: fmt.Sprintf("%s, %d numbers for trunk %s",
			header.Filename, len(parsed.Numbers), on.Number),
	})

	writeJSON(w, http.StatusOK, preview)
}

// didChange is one number and what would happen to it.
type didChange struct {
	Number string `json:"number"`
	// Extension is the number the sheet said it should ring, empty when the
	// sheet only gave a DID.
	Extension string `json:"extension,omitempty"`
	// Existing is what the DID already does, on the buckets where that is the
	// point: the trunk it is already on, or the extension it already rings.
	Existing string `json:"existing,omitempty"`
	Row      int    `json:"row"`
}

/*
comparePreview sorts every parsed number into what would happen to it.

Pure, and takes what the phone system said as an argument rather than fetching
it, so the decision each number lands on can be tested without a PBX.
*/
func comparePreview(parsed bulk.DIDs, on didTrunk, all []didTrunk) map[string]any {
	onTrunk := make(map[string]bool, len(on.DIDs))
	for _, did := range on.DIDs {
		onTrunk[did] = true
	}
	// Where else each number lives. The trunk being imported onto is skipped
	// so a number on it does not report itself as being somewhere else.
	elsewhere := map[string]string{}
	for _, t := range all {
		if t.ID == on.ID {
			continue
		}
		for _, did := range t.DIDs {
			elsewhere[did] = t.name()
		}
	}

	adding, already := []didChange{}, []didChange{}
	other, routed := []didChange{}, []didChange{}

	for _, did := range parsed.Numbers {
		change := didChange{Number: did.Number, Extension: did.Extension, Row: did.Row}

		// Whether it is already assigned is asked first, because it is what
		// the mode decides about and it is true of numbers in both of the
		// first two buckets.
		if to, taken := on.Routes[did.Number]; taken && did.Extension != "" && to != did.Extension {
			routed = append(routed, didChange{
				Number: did.Number, Extension: did.Extension, Existing: to, Row: did.Row,
			})
		}

		switch {
		case onTrunk[did.Number]:
			already = append(already, change)
		default:
			adding = append(adding, change)
			if where, clash := elsewhere[did.Number]; clash {
				other = append(other, didChange{
					Number: did.Number, Extension: did.Extension, Existing: where, Row: did.Row,
				})
			}
		}
	}

	// How many would end up newly assigned, which is the number somebody
	// actually reads before confirming. Counted across both buckets, because a
	// DID already on the trunk with no rule is assigned by this import just as
	// much as a new one is.
	willRoute := 0
	for _, change := range append(append([]didChange{}, adding...), already...) {
		if change.Extension == "" {
			continue
		}
		if _, taken := on.Routes[change.Number]; taken {
			continue
		}
		willRoute++
	}

	return map[string]any{
		"adding":        adding,
		"already":       already,
		"on_other":      other,
		"already_route": routed,
		"refused":       parsed.Refused,
		"repeated":      parsed.Repeated,
		"counts": map[string]int{
			"adding":     len(adding),
			"already":    len(already),
			"on_other":   len(other),
			"routing":    willRoute,
			"kept_rules": len(routed),
			"refused":    len(parsed.Refused),
			"total":      len(parsed.Numbers),
		},
	}
}

// name is what to call a trunk on a screen, from the fields the plugin sent.
func (t didTrunk) name() string {
	if name := strings.TrimSpace(t.Name); name != "" {
		return name
	}
	return t.Number
}

// pickTrunk resolves what the screen sent — an id or a trunk number.
func pickTrunk(trunks []didTrunk, want string) (didTrunk, bool) {
	want = strings.TrimSpace(want)
	if want == "" {
		return didTrunk{}, false
	}
	for _, t := range trunks {
		if want == t.Number || want == fmt.Sprint(t.ID) {
			return t, true
		}
	}
	return didTrunk{}, false
}

/*
importDIDs applies what a person approved.

The numbers arrive in the body rather than being read back from a stored plan,
which is deliberate. There is nothing held between the preview and this, so the
only thing that can be applied is the list that was on the screen — no row can
have appeared in between, because there is no in between.

The write itself is the plugin's, through the capability, so the merge that
keeps the trunk's existing numbers happens in one place and this never assembles
a DID array of its own.
*/
func (s *Server) importDIDs(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, ok := s.didCustomer(w, r)
	if !ok {
		return
	}

	var body struct {
		Trunk   string `json:"trunk"`
		Confirm string `json:"confirm"`
		/*
			Mode is what to do with a DID that is already assigned: "append"
			leaves it exactly as it is, "replace" repoints it.

			Passed through rather than decided here. The plugin is what knows
			whether a DID is assigned, and a second opinion about it in this
			file would be a second place to keep in step.
		*/
		Mode string `json:"mode"`
		DIDs []struct {
			Number    string `json:"number"`
			Extension string `json:"extension"`
		} `json:"dids"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	if !strings.EqualFold(strings.TrimSpace(body.Confirm), "import") {
		writeJSON(w, http.StatusBadRequest, errBody(
			`Type "import" to confirm. Nothing has been changed.`))
		return
	}
	if strings.TrimSpace(body.Trunk) == "" {
		writeJSON(w, http.StatusBadRequest, errBody("say which trunk these go on"))
		return
	}
	if len(body.DIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody("there are no numbers to import"))
		return
	}
	if len(body.DIDs) > bulk.MaxDIDs {
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Sprintf(
			"that is more than %d numbers; do it in smaller batches", bulk.MaxDIDs)))
		return
	}

	tool, err := s.approvedTool(r.Context(), plugin.CapPhoneTrunkDidImport, true)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody(
			"nothing in this deployment can add numbers to a trunk"))
		return
	}

	args, err := json.Marshal(map[string]any{
		"trunk": body.Trunk, "mode": body.Mode, "dids": body.DIDs,
	})
	if err != nil {
		s.fail(w, err, "could not prepare that import")
		return
	}

	answer, err := s.performTool(r.Context(), actor, tool, args, &customerID, fromSheet)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "dids.import",
		Outcome:     audit.OutcomeOK,
		CustomerID:  &customerID,
		Detail: fmt.Sprintf("%d numbers onto trunk %s, %s", len(body.DIDs), body.Trunk,
			importMode(body.Mode)),
	})

	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(answer)
}

// importMode says which way an import was run, for the activity log. Written
// out in words there rather than as a flag, because "replacing where already
// assigned" is the part of that line somebody reads it for.
func importMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "replace") {
		return "replacing where already assigned"
	}
	return "leaving existing assignments alone"
}

/*
startingDIDs hands back what the trunk carries now, as a file to edit.

The round trip the extension flow already has: download what is there, fix it
in a spreadsheet, upload the difference. For DIDs it is also the only way to
see a trunk's whole list without paging a console, and it is what somebody
reaches for when the carrier's file and the trunk have drifted.

Written with the header, because a file that comes back out of Azir should read
as one when it goes into Excel. The headerless form is for what a carrier
sends, not for what this produces.
*/
func (s *Server) startingDIDs(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, ok := s.didCustomer(w, r)
	if !ok {
		return
	}

	trunks, err := s.currentTrunks(r.Context(), actor, customerID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(
			"could not read the phone system: "+err.Error()))
		return
	}
	on, found := pickTrunk(trunks, r.URL.Query().Get("trunk"))
	if !found {
		writeJSON(w, http.StatusNotFound, errBody("this phone system has no such trunk"))
		return
	}

	numbers := append([]string(nil), on.DIDs...)
	sort.Strings(numbers)

	// The same two columns the upload reads, so a file that comes out of here
	// goes back in without anybody rearranging it.
	var out strings.Builder
	out.WriteString("did,extension\n")
	for _, number := range numbers {
		out.WriteString(csvCell(number))
		out.WriteString(",")
		out.WriteString(csvCell(on.Routes[number]))
		out.WriteString("\n")
	}

	w.Header().Set("content-type", "text/csv; charset=utf-8")
	w.Header().Set("content-disposition", fmt.Sprintf(
		`attachment; filename="dids-%s.csv"`, safeFilename(on.name())))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(out.String()))
}

// csvCell quotes a value when it has to be. A destination never contains a
// comma today, and writing this out is cheaper than the bug when one does.
func csvCell(value string) string {
	if !strings.ContainsAny(value, `,"`+"\n") {
		return value
	}
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

// safeFilename keeps a trunk's name usable in a Content-Disposition header.
func safeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "trunk"
	}
	return b.String()
}

// didCustomer reads and checks the customer every route here needs.
func (s *Server) didCustomer(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
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
