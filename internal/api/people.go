package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/store"
)

/*
Managing accounts after they exist.

The store could already change a role and disable somebody; nothing exposed it,
so an account was whatever it was created as, forever. Everything here is behind
user.manage, which is the permission that lets a person hand out permissions —
the one worth being careful with.

Two rules run through all of it, and both are about the same failure: a
deployment nobody can administer. There is no recovery from that short of the
database, so the checks live on the server where they cannot be skipped, and
they are checked before anything is written rather than after.
*/

// lockoutError is refusing to do something that would leave nobody in charge.
func lockoutError(w http.ResponseWriter, why string) {
	writeJSON(w, http.StatusConflict, map[string]string{"error": why})
}

// lastAdminStanding reports whether this account is the only administrator left.
func (s *Server) lastAdminStanding(r *http.Request, target store.User) (bool, error) {
	if target.Role != identity.RoleAdmin {
		return false, nil
	}
	admins, err := s.DB.CountAdmins(r.Context())
	if err != nil {
		return false, err
	}
	return admins <= 1, nil
}

// userByID finds the account a request is about.
func (s *Server) userByID(r *http.Request) (store.User, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return store.User{}, errors.New("that is not an account id")
	}
	users, err := s.DB.ListUsers(r.Context())
	if err != nil {
		return store.User{}, err
	}
	for _, u := range users {
		if u.ID == id {
			return u, nil
		}
	}
	return store.User{}, errNoSuchUser
}

var errNoSuchUser = errors.New("no such account")

type updateUserRequest struct {
	Role     *string `json:"role,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	target, err := s.userByID(r)
	if errors.Is(err, errNoSuchUser) {
		writeJSON(w, http.StatusNotFound, errBody("no such account"))
		return
	}
	if err != nil {
		s.fail(w, err, "could not read that account")
		return
	}

	var body updateUserRequest
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	if body.Role != nil && *body.Role != target.Role {
		alone, err := s.lastAdminStanding(r, target)
		if err != nil {
			s.fail(w, err, "could not check the administrators")
			return
		}
		if alone {
			lockoutError(w, "this is the only administrator; make somebody else one first")
			return
		}
		if err := s.DB.SetUserRole(r.Context(), target.ID, strings.TrimSpace(*body.Role)); err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
			return
		}
		s.Audit.Record(r.Context(), audit.Event{
			ActorUserID: actor.Email, Action: "user.role",
			Outcome: audit.OutcomeOK, Detail: target.Email + " → " + *body.Role,
		})
	}

	if body.Disabled != nil && *body.Disabled != target.Disabled {
		if *body.Disabled {
			// Locking yourself out is the one mistake with no way back from
			// inside the application.
			if target.Email == actor.Email {
				lockoutError(w, "you cannot disable your own account")
				return
			}
			alone, err := s.lastAdminStanding(r, target)
			if err != nil {
				s.fail(w, err, "could not check the administrators")
				return
			}
			if alone {
				lockoutError(w, "this is the only administrator; make somebody else one first")
				return
			}
		}
		if err := s.DB.SetUserDisabled(r.Context(), target.ID, *body.Disabled); err != nil {
			s.fail(w, err, "could not change that account")
			return
		}
		// Written out rather than built from a variable. An action name that
		// only exists at runtime cannot be checked against the words the
		// console shows for it, and these two had gone unnamed for exactly
		// that reason.
		action := "user.enabled"
		if *body.Disabled {
			action = "user.disabled"
		}
		s.Audit.Record(r.Context(), audit.Event{
			ActorUserID: actor.Email, Action: action,
			Outcome: audit.OutcomeOK, Detail: target.Email,
		})
	}

	users, err := s.DB.ListUsers(r.Context())
	if err != nil {
		s.fail(w, err, "could not read the accounts")
		return
	}
	writeJSON(w, http.StatusOK, users)
}

type setPasswordRequest struct {
	Password string `json:"password"`
}

/*
setPassword replaces somebody's password.

An administrator setting it directly, rather than a reset link, because there is
no mail server in this deployment and inventing one to change a password would
be a great deal of machinery for a team of technicians who sit near each other.
The new password is handed over out of band, exactly as it is at first run.
*/
func (s *Server) setPassword(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	target, err := s.userByID(r)
	if errors.Is(err, errNoSuchUser) {
		writeJSON(w, http.StatusNotFound, errBody("no such account"))
		return
	}
	if err != nil {
		s.fail(w, err, "could not read that account")
		return
	}

	var body setPasswordRequest
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	if len(body.Password) < identity.MinPasswordLength {
		writeJSON(w, http.StatusBadRequest,
			errBody("a password needs at least 12 characters"))
		return
	}

	if err := s.DB.SetUserPassword(r.Context(), target.ID, body.Password); err != nil {
		s.fail(w, err, "could not set that password")
		return
	}

	// Every existing session ends. A password changed because somebody has left
	// or an account was misused is not a password change if the old session
	// keeps working.
	ended, err := s.DB.EndSessionsFor(r.Context(), target.ID)
	if err != nil {
		s.Log.Warn("could not end sessions after a password change", "error", err)
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "user.password",
		Outcome: audit.OutcomeOK, Detail: target.Email,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions_ended": ended})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	target, err := s.userByID(r)
	if errors.Is(err, errNoSuchUser) {
		writeJSON(w, http.StatusNotFound, errBody("no such account"))
		return
	}
	if err != nil {
		s.fail(w, err, "could not read that account")
		return
	}

	if target.Email == actor.Email {
		lockoutError(w, "you cannot remove your own account")
		return
	}
	alone, err := s.lastAdminStanding(r, target)
	if err != nil {
		s.fail(w, err, "could not check the administrators")
		return
	}
	if alone {
		lockoutError(w, "this is the only administrator; make somebody else one first")
		return
	}

	if err := s.DB.DeleteUser(r.Context(), target.ID); err != nil {
		s.fail(w, err, "could not remove that account")
		return
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "user.remove",
		Outcome: audit.OutcomeOK, Detail: target.Email,
	})
	w.WriteHeader(http.StatusNoContent)
}
