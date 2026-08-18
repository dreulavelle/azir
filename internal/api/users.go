package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/store"
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

/*
Roles, as something an administrator can shape.

role.manage has existed as a permission since the beginning and guarded nothing
— it could be granted, it appeared in the grid, and no route asked for it. These
are what it is for.

Separate from user.manage on purpose. Deciding who works here and deciding what
a technician is trusted with are different decisions, and an MSP that lets a
senior technician add accounts does not necessarily want them widening what
their own role may do.
*/

func (s *Server) createRole(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	var body struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	role, err := s.DB.CreateRole(r.Context(), body.Name, body.Description, body.Permissions)
	if errors.Is(err, store.ErrRoleExists) {
		writeJSON(w, http.StatusConflict, errBody("A role with that name already exists."))
		return
	}
	if err != nil {
		s.fail(w, err, "could not create the role")
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "role.create",
		Outcome:     audit.OutcomeOK,
		Detail:      role.Name + ": " + strings.Join(role.Permissions, ", "),
	})
	writeJSON(w, http.StatusOK, role)
}

func (s *Server) updateRole(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	var body struct {
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	name := r.PathValue("name")

	// Not your own.
	//
	// role.manage is otherwise a way to become an administrator in one
	// request: hold it, add credential.manage to the role you are already in,
	// and the vault opens on the next call. Somebody else with the permission
	// can still change this role, and an administrator always can — what is
	// refused is widening your own reach without anybody else involved.
	if name == actor.Role {
		writeJSON(w, http.StatusForbidden, errBody(
			"You cannot change your own role. Ask another administrator."))
		return
	}

	role, err := s.DB.UpdateRole(r.Context(), name, body.Description, body.Permissions)
	if errors.Is(err, store.ErrRoleProtected) {
		writeJSON(w, http.StatusForbidden, errBody(
			"The admin role cannot be changed. It is how you get back in if something else goes wrong."))
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errBody("No such role."))
		return
	}
	if err != nil {
		s.fail(w, err, "could not save the role")
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "role.update",
		Outcome:     audit.OutcomeOK,
		Detail:      role.Name + ": " + strings.Join(role.Permissions, ", "),
	})
	writeJSON(w, http.StatusOK, role)
}

func (s *Server) deleteRole(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	name := r.PathValue("name")

	err := s.DB.DeleteRole(r.Context(), name)
	if errors.Is(err, store.ErrRoleProtected) {
		writeJSON(w, http.StatusForbidden, errBody(
			"The admin role cannot be removed. It is how you get back in if something else goes wrong."))
		return
	}
	if errors.Is(err, store.ErrRoleInUse) {
		// Said with the number, because "still in use" is something somebody
		// then has to go and work out for themselves.
		accounts, isDefault, countErr := s.DB.RoleUsers(r.Context(), name)
		reason := "Something still uses this role."
		switch {
		case countErr != nil:
		case accounts > 0 && isDefault:
			reason = fmt.Sprintf("%d %s use this role, and it is what single sign-on gives new accounts. Move them first.",
				accounts, plural(accounts, "account", "accounts"))
		case accounts > 0:
			reason = fmt.Sprintf("%d %s still %s this role. Move them first.",
				accounts, plural(accounts, "account", "accounts"), plural(accounts, "uses", "use"))
		case isDefault:
			reason = "Single sign-on gives this role to new accounts. Change that first."
		}
		writeJSON(w, http.StatusConflict, errBody(reason))
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errBody("No such role."))
		return
	}
	if err != nil {
		s.fail(w, err, "could not remove the role")
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "role.delete",
		Outcome:     audit.OutcomeOK,
		Detail:      name,
	})
	writeJSON(w, http.StatusOK, map[string]any{"removed": name})
}

// plural picks the word to go with a count.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
