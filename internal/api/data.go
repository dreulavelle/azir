package api

import (
	"net/http"
	"strings"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
)

/*
What Azir is holding, and clearing it.

Two endpoints behind one permission, because the reason to look and the reason
to clear are the same reason: somebody is accounting for the customer data
their tooling has accumulated. Splitting the read from the write would only
produce a role that can be told what is stored and not allowed to do anything
about it.
*/

func (s *Server) getDataUsage(w http.ResponseWriter, r *http.Request) {
	kinds, total, err := s.DB.DataUsage(r.Context())
	if err != nil {
		s.fail(w, err, "could not read what is stored")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stored": kinds,
		"total":  total,
	})
}

/*
clearWork empties everything Azir has done and keeps everything it was set up
with.

Guarded by a typed confirmation rather than a second click. A dialog that only
needs one more press is a dialog people learn to press, and this one deletes
every conversation and the audit trail that would have said what happened. The
word has to be typed, the server checks it, and a request without it is refused
even though the button in the console would not have sent one — because the
button is not the only thing that can call this.
*/
func (s *Server) clearWork(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	var body struct {
		Confirm string `json:"confirm"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	if !strings.EqualFold(strings.TrimSpace(body.Confirm), "clear") {
		writeJSON(w, http.StatusBadRequest, errBody(
			`Type "clear" to confirm. Nothing has been removed.`))
		return
	}

	removed, err := s.DB.ClearWork(r.Context())
	if err != nil {
		s.fail(w, err, "could not clear")
		return
	}

	// After the clear, not before: this is the one entry that should be in an
	// otherwise empty log, and recording it first would delete it.
	var said []string
	var rows int64
	for _, part := range removed {
		rows += part.Rows
		if part.Rows > 0 {
			said = append(said, part.Name)
		}
	}
	detail := "nothing was stored"
	if len(said) > 0 {
		detail = strings.Join(said, ", ")
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "data.clear",
		Outcome:     audit.OutcomeOK,
		Detail:      detail,
	})

	s.Log.Warn("cleared stored work", "actor", actor.Email, "rows", rows)
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}
