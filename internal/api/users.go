package api

import (
	"net/http"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
)

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.DB.ListUsers(r.Context())
	if err != nil {
		s.fail(w, err, "could not list users")
		return
	}
	writeJSON(w, http.StatusOK, users)
}

// listRoles returns every role with its permissions, plus the full permission
// vocabulary so the console can render a role editor without hardcoding it.
func (s *Server) listRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.DB.ListRoles(r.Context())
	if err != nil {
		s.fail(w, err, "could not list roles")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"roles":           roles,
		"all_permissions": identity.AllPermissions,
	})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	var body struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
		Password    string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	if body.Role == "" {
		body.Role = "technician"
	}

	user, err := s.DB.CreateUser(r.Context(), body.Email, body.DisplayName, body.Role, body.Password)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "user.create",
		Outcome:     audit.OutcomeOK,
		Detail:      user.Email + " as " + user.Role,
	})
	writeJSON(w, http.StatusCreated, user)
}
