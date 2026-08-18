package api

import (
	"errors"
	"net/http"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/store"
)

/*
Where accounts are signed in, and ending it.

Every sign-in has always recorded the browser and the address, and nothing ever
read either. The question this answers is the one asked after a laptop goes
missing: where is this signed in, and can I stop it.

Behind user.manage, because it is about the accounts rather than about the
person asking. Someone who can add and remove accounts can already end anybody's
access; being able to see where they are signed in is strictly less than that.
*/

// currentSession is the hash of the token making this request, so the list can
// mark it and nobody signs themselves out wondering why the page broke.
func currentSession(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return identity.HashToken(cookie.Value)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.DB.ListSessions(r.Context(), currentSession(r))
	if err != nil {
		s.fail(w, err, "could not read the open sessions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (s *Server) endSession(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	id := r.PathValue("id")

	// Read before ending, so the log can say whose it was rather than naming a
	// hash that means nothing to anybody reading it later.
	var whose string
	if all, err := s.DB.ListSessions(r.Context(), ""); err == nil {
		for _, open := range all {
			if open.ID == id {
				whose = open.Email
				break
			}
		}
	}

	if err := s.DB.EndSession(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errBody("That session has already ended."))
			return
		}
		s.fail(w, err, "could not end the session")
		return
	}

	if whose == "" {
		whose = "an account"
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "session.end",
		Outcome:     audit.OutcomeOK,
		Detail:      whose,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ended": true})
}
