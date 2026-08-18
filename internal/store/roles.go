package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/dreulavelle/azir/internal/identity"
)

/*
Roles, and the two rules that keep somebody from locking themselves out.

A role is a name and a set of permissions, and everything in Azir is checked
against the permission rather than the name — so a new role with its own mix is
a setting rather than a change to the code. That was always true; what was
missing was any way to make one.

The rules:

The admin role cannot be changed or removed. It is the way back into a
deployment where something else has gone wrong, and a console that lets you
edit your own way out of administering it is a console that eventually will.
Every other role, including the other two that ship, is yours to shape.

A role in use cannot be removed. Accounts point at it and so does the setting
that says what single sign-on hands new arrivals; taking it away underneath
either is how somebody ends up with an account that cannot be resolved at all.
*/

// protectedRole is the one that cannot be edited or removed.
const protectedRole = "admin"

var (
	// ErrRoleProtected means someone tried to change or remove admin.
	ErrRoleProtected = errors.New("store: the admin role cannot be changed")
	// ErrRoleInUse means accounts or single sign-on still point at it.
	ErrRoleInUse = errors.New("store: that role is still in use")
	// ErrRoleExists means the name is taken.
	ErrRoleExists = errors.New("store: a role with that name already exists")
)

// roleName is what a name is allowed to look like once it is stored.
var roleName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

/*
NameRole turns what somebody typed into the identifier it is stored as.

Lowercase and hyphenated, because the stored name is a foreign key and travels
in the API — "Senior Tech" and "senior tech" being two different roles is a
problem nobody would guess they had. The console puts the capitals back when it
shows it.
*/
func NameRole(typed string) string {
	slug := strings.ToLower(strings.TrimSpace(typed))
	slug = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(slug, "-")
	return strings.Trim(slug, "-")
}

// cleanPermissions keeps the ones Azir actually checks, in a stable order, with
// no repeats. A permission the server has never heard of would sit in the row
// looking granted and be checked by nothing.
func cleanPermissions(wanted []string) []string {
	asked := make(map[string]bool, len(wanted))
	for _, p := range wanted {
		asked[strings.TrimSpace(p)] = true
	}
	out := make([]string, 0, len(wanted))
	for _, known := range identity.AllPermissions {
		if asked[known] {
			out = append(out, known)
		}
	}
	return out
}

// CreateRole adds a role.
func (db *DB) CreateRole(ctx context.Context, name, description string, permissions []string) (Role, error) {
	slug := NameRole(name)
	if !roleName.MatchString(slug) {
		return Role{}, errors.New("store: a role needs a name of letters or numbers")
	}
	if slug == protectedRole {
		return Role{}, ErrRoleExists
	}

	role := Role{
		Name:        slug,
		Description: strings.TrimSpace(description),
		Permissions: cleanPermissions(permissions),
	}
	_, err := db.pool.Exec(ctx,
		`INSERT INTO roles (name, description, permissions, builtin)
		 VALUES ($1, $2, $3, false)`,
		role.Name, role.Description, role.Permissions)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return Role{}, ErrRoleExists
		}
		return Role{}, fmt.Errorf("store: create role: %w", err)
	}
	return role, nil
}

// UpdateRole changes what a role may do, and what it says it is for. The name
// is not changed: accounts and the sign-on default point at it, and a rename
// would have to move them all to mean anything.
func (db *DB) UpdateRole(ctx context.Context, name, description string, permissions []string) (Role, error) {
	if name == protectedRole {
		return Role{}, ErrRoleProtected
	}
	role := Role{
		Name:        name,
		Description: strings.TrimSpace(description),
		Permissions: cleanPermissions(permissions),
	}
	tag, err := db.pool.Exec(ctx,
		`UPDATE roles SET description = $2, permissions = $3 WHERE name = $1`,
		role.Name, role.Description, role.Permissions)
	if err != nil {
		return Role{}, fmt.Errorf("store: update role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Role{}, ErrNotFound
	}
	if err := db.pool.QueryRow(ctx,
		`SELECT builtin FROM roles WHERE name = $1`, name).Scan(&role.Builtin); err != nil {
		return Role{}, err
	}
	return role, nil
}

/*
RoleUsers reports what still points at a role.

Read before removing one, and reported back rather than turned into a bare
refusal — "3 accounts still use this" is something somebody can act on, and
"that role is in use" is something they have to go and work out.
*/
func (db *DB) RoleUsers(ctx context.Context, name string) (accounts int, isSignOnDefault bool, err error) {
	err = db.pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM users WHERE role = $1),
		        EXISTS (SELECT 1 FROM auth_config WHERE default_role = $1)`,
		name).Scan(&accounts, &isSignOnDefault)
	if err != nil {
		return 0, false, fmt.Errorf("store: role in use: %w", err)
	}
	return accounts, isSignOnDefault, nil
}

// DeleteRole removes a role nothing is using.
func (db *DB) DeleteRole(ctx context.Context, name string) error {
	if name == protectedRole {
		return ErrRoleProtected
	}
	accounts, isDefault, err := db.RoleUsers(ctx, name)
	if err != nil {
		return err
	}
	if accounts > 0 || isDefault {
		return ErrRoleInUse
	}

	tag, err := db.pool.Exec(ctx, `DELETE FROM roles WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("store: delete role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
