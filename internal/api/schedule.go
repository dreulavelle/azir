package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/pkg/plugin"
)

/*
When a customer's phone system is closed.

Holidays and early closings are the same thing to a phone system — a span of
dates, sometimes narrowed to a span of hours, sometimes repeating every year —
so they are one screen and one set of endpoints here.

The same gates as everywhere else: reads go through the approval a technician
gave the tool, writes go through that and phone.manage and land in the activity
log. Guarded by phone.manage throughout.
*/

// getSchedule answers the screen: every closure, and the hours they interrupt.
func (s *Server) getSchedule(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, ok := s.customerOf(w, r)
	if !ok {
		return
	}

	tool, err := s.approvedTool(r.Context(), plugin.CapPhoneSchedule, false)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
		return
	}
	// Which department. Hours and holidays belong to one, not to the company,
	// so the screen asks for one and the plugin answers for it.
	asked, err := json.Marshal(map[string]string{
		"department": strings.TrimSpace(r.URL.Query().Get("department")),
	})
	if err != nil {
		s.fail(w, err, "could not ask for that department")
		return
	}
	raw, err := s.readTool(r.Context(), actor, tool, asked, &customerID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(
			"could not read the schedule: "+err.Error()))
		return
	}

	// Passed through as the plugin shaped it. Azir does not know what a
	// closure is beyond a name and some dates, and inventing a second opinion
	// here is how the two drift apart.
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(raw); err != nil {
		return
	}
}

// addSchedule puts one closure on a phone system.
func (s *Server) addSchedule(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, ok := s.customerOf(w, r)
	if !ok {
		return
	}

	var body struct {
		Name       string `json:"name"`
		Starts     string `json:"starts"`
		Ends       string `json:"ends"`
		FromTime   string `json:"from_time"`
		ToTime     string `json:"to_time"`
		Repeats    bool   `json:"repeats"`
		Department string `json:"department"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.Starts) == "" {
		writeJSON(w, http.StatusBadRequest, errBody("a closure needs a name and a first day"))
		return
	}

	tool, err := s.approvedTool(r.Context(), plugin.CapPhoneScheduleAdd, true)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
		return
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		s.fail(w, err, "could not prepare that closure")
		return
	}
	raw, err := s.performTool(r.Context(), actor, tool, encoded, &customerID, byHand)
	if err != nil {
		// The phone system's own refusal, which by this point is a sentence:
		// a name already used, or dates that overlap something.
		writeJSON(w, http.StatusConflict, errBody(err.Error()))
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "schedule.add",
		Outcome:     audit.OutcomeOK,
		CustomerID:  &customerID,
		Detail:      strings.TrimSpace(body.Name) + ", from " + strings.TrimSpace(body.Starts),
	})

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(raw); err != nil {
		return
	}
}

/*
setHours writes a department's weekly office hours.

Its own endpoint because it is its own thing. The hours are the week a
department keeps; the closures are the dated exceptions to it. Changing the
week is a different act from saying that one Friday is not normal, and folding
them together would turn "close early this Friday" into a change somebody has
to remember to undo.
*/
func (s *Server) setHours(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, ok := s.customerOf(w, r)
	if !ok {
		return
	}

	var body struct {
		Department string `json:"department"`
		Days       []struct {
			Day  string `json:"day"`
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"days"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	tool, err := s.approvedTool(r.Context(), plugin.CapPhoneHoursSet, true)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
		return
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		s.fail(w, err, "could not prepare those hours")
		return
	}
	raw, err := s.performTool(r.Context(), actor, tool, encoded, &customerID, byHand)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "schedule.hours",
		Outcome:     audit.OutcomeOK,
		CustomerID:  &customerID,
		Detail: fmt.Sprintf("%s: %d open day(s)",
			strings.TrimSpace(body.Department), len(body.Days)),
	})

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(raw); err != nil {
		return
	}
}

/*
removeSchedule takes one closure off a phone system.

By id, one at a time. Removing the wrong closure leaves a business open on a
day it meant to be shut, which nobody finds out about until the phone rings.
*/
func (s *Server) removeSchedule(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, ok := s.customerOf(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, errBody("say which closure to remove"))
		return
	}

	tool, err := s.approvedTool(r.Context(), plugin.CapPhoneScheduleRemove, true)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
		return
	}
	encoded, _ := json.Marshal(map[string]any{"id": id})
	if _, err := s.performTool(r.Context(), actor, tool, encoded, &customerID, byHand); err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "schedule.remove",
		Outcome:     audit.OutcomeOK,
		CustomerID:  &customerID,
		Detail:      "closure " + strconv.FormatInt(id, 10),
	})
	writeJSON(w, http.StatusOK, map[string]any{"removed": id})
}
