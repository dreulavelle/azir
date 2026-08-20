package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/dreulavelle/azir/internal/identity"
)

// User is an account.
type User struct {
	ID          uuid.UUID  `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	Role        string     `json:"role"`
	Provider    *string    `json:"provider,omitempty"`
	Disabled    bool       `json:"disabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
}

// Role is a named set of permissions.
type Role struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	Builtin     bool     `json:"builtin"`
}

// CountUsers reports how many accounts exist, so first-run setup can be
// offered exactly once.
func (db *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count users: %w", err)
	}
	return n, nil
}

// CreateUser adds an account. Pass an empty password for a provider-backed
// account, which then cannot be signed into locally.
func (db *DB) CreateUser(ctx context.Context, email, displayName, role, password string) (User, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || !strings.Contains(email, "@") {
		return User{}, errors.New("store: a valid email is required")
	}

	var hash *string
	if password != "" {
		encoded, err := identity.HashPassword(password)
		if err != nil {
			return User{}, err
		}
		hash = &encoded
	}

	u := User{
		ID:          uuid.New(),
		Email:       email,
		DisplayName: strings.TrimSpace(displayName),
		Role:        role,
	}
	err := db.pool.QueryRow(ctx, `
		INSERT INTO users (id, email, display_name, role, password_hash)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at`,
		u.ID, u.Email, u.DisplayName, u.Role, hash,
	).Scan(&u.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "users_email_key") {
			return User{}, fmt.Errorf("store: an account already exists for %s", email)
		}
		if strings.Contains(err.Error(), "users_role_fkey") {
			return User{}, fmt.Errorf("store: no such role %q", role)
		}
		return User{}, fmt.Errorf("store: create user: %w", err)
	}
	return u, nil
}

// Authenticate verifies an email and password and returns the actor.
//
// The same error is returned for an unknown account and a wrong password, so
// the response cannot be used to discover which emails exist. The password is
// verified even when no account is found, so the timing does not either.
func (db *DB) Authenticate(ctx context.Context, email, password string) (identity.Actor, error) {
	email = strings.TrimSpace(strings.ToLower(email))

	var (
		id           uuid.UUID
		displayName  string
		role         string
		passwordHash *string
		disabled     bool
		permissions  []string
	)
	err := db.pool.QueryRow(ctx, `
		SELECT u.id, u.display_name, u.role, u.password_hash, u.disabled, r.permissions
		FROM users u JOIN roles r ON r.name = u.role
		WHERE lower(u.email) = $1`, email,
	).Scan(&id, &displayName, &role, &passwordHash, &disabled, &permissions)

	if errors.Is(err, pgx.ErrNoRows) {
		// Hash anyway: a fast rejection here and a slow one for a real account
		// is enough to enumerate users.
		identity.VerifyPassword(dummyHash, password)
		return identity.Actor{}, identity.ErrBadCredentials
	}
	if err != nil {
		return identity.Actor{}, fmt.Errorf("store: authenticate: %w", err)
	}
	if disabled || passwordHash == nil {
		identity.VerifyPassword(dummyHash, password)
		return identity.Actor{}, identity.ErrBadCredentials
	}
	if !identity.VerifyPassword(*passwordHash, password) {
		return identity.Actor{}, identity.ErrBadCredentials
	}

	return identity.Actor{
		UserID:      id,
		Email:       email,
		DisplayName: displayName,
		Role:        role,
		Permissions: permissions,
	}, nil
}

// dummyHash is a real argon2id hash of an unusable password, used so a failed
// lookup costs the same as a failed password check.
const dummyHash = "argon2id$2$65536$4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// CreateSession issues a session and returns the token to hand to the browser.
func (db *DB) CreateSession(ctx context.Context, userID uuid.UUID, userAgent, ip string) (string, time.Time, error) {
	token, hash, err := identity.NewSessionToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().Add(identity.SessionLifetime)

	if _, err := db.pool.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, expires_at, user_agent, ip)
		VALUES ($1, $2, $3, $4, $5)`,
		hash, userID, expires, truncateText(userAgent, 400), ip,
	); err != nil {
		return "", time.Time{}, fmt.Errorf("store: create session: %w", err)
	}
	return token, expires, nil
}

// ActorForSession resolves a session token to an actor.
func (db *DB) ActorForSession(ctx context.Context, token string) (identity.Actor, error) {
	var (
		a           identity.Actor
		disabled    bool
		permissions []string
	)
	err := db.pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.display_name, u.role, u.disabled, r.permissions
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		JOIN roles r ON r.name = u.role
		WHERE s.token_hash = $1 AND s.expires_at > now()`,
		identity.HashToken(token),
	).Scan(&a.UserID, &a.Email, &a.DisplayName, &a.Role, &disabled, &permissions)

	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Actor{}, identity.ErrUnauthenticated
	}
	if err != nil {
		return identity.Actor{}, fmt.Errorf("store: resolve session: %w", err)
	}
	if disabled {
		// A disabled account loses access immediately rather than when its
		// session happens to expire.
		return identity.Actor{}, identity.ErrUnauthenticated
	}
	a.Permissions = permissions

	// Best effort: knowing when someone was last active is useful, and failing
	// a request because that write failed would not be.
	_, _ = db.pool.Exec(ctx, `UPDATE users SET last_seen_at = now() WHERE id = $1`, a.UserID)

	return a, nil
}

// DeleteSession signs one session out.
func (db *DB) DeleteSession(ctx context.Context, token string) error {
	_, err := db.pool.Exec(ctx,
		`DELETE FROM sessions WHERE token_hash = $1`, identity.HashToken(token))
	return err
}

// PruneSessions removes expired sessions.
func (db *DB) PruneSessions(ctx context.Context) (int64, error) {
	tag, err := db.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ListUsers returns every account.
func (db *DB) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT id, email, display_name, role, provider, disabled, created_at, last_seen_at
		FROM users ORDER BY email`)
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	defer rows.Close()

	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role,
			&u.Provider, &u.Disabled, &u.CreatedAt, &u.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListRoles returns every role and its permissions.
func (db *DB) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT name, description, permissions, builtin FROM roles ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list roles: %w", err)
	}
	defer rows.Close()

	out := []Role{}
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.Name, &r.Description, &r.Permissions, &r.Builtin); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetUserRole changes an account's role.
func (db *DB) SetUserRole(ctx context.Context, id uuid.UUID, role string) error {
	tag, err := db.pool.Exec(ctx, `UPDATE users SET role = $2 WHERE id = $1`, id, role)
	if err != nil {
		return fmt.Errorf("store: set role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserDisabled enables or disables an account.
func (db *DB) SetUserDisabled(ctx context.Context, id uuid.UUID, disabled bool) error {
	tag, err := db.pool.Exec(ctx, `UPDATE users SET disabled = $2 WHERE id = $1`, id, disabled)
	if err != nil {
		return fmt.Errorf("store: set disabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	// Disabling revokes immediately rather than at session expiry.
	if disabled {
		_, _ = db.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id)
	}
	return nil
}

// CountAdmins reports how many enabled admins exist, so the last one cannot be
// removed or demoted and lock everyone out.
func (db *DB) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := db.pool.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE role = $1 AND NOT disabled`, identity.RoleAdmin).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count admins: %w", err)
	}
	return n, nil
}

func truncateText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}

// SetUserPassword replaces somebody's password.
func (db *DB) SetUserPassword(ctx context.Context, id uuid.UUID, password string) error {
	hash, err := identity.HashPassword(password)
	if err != nil {
		return err
	}
	tag, err := db.pool.Exec(ctx,
		`UPDATE users SET password_hash = $2 WHERE id = $1`, id, hash)
	if err != nil {
		return fmt.Errorf("store: set password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// EndSessionsFor signs somebody out everywhere, and reports how many sessions
// that was. Used when a password changes: a credential replaced because an
// account was misused has not been replaced if the old session still works.
func (db *DB) EndSessionsFor(ctx context.Context, id uuid.UUID) (int64, error) {
	tag, err := db.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id)
	if err != nil {
		return 0, fmt.Errorf("store: end sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteUser removes an account and everything hanging off it.
func (db *DB) DeleteUser(ctx context.Context, id uuid.UUID) error {
	tag, err := db.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

/*
ActorByID resolves a person and what they may do, without a session.

For work that runs when nobody is signed in. A job carries the person who
scheduled it, and their permissions are read again at the moment it fires
rather than frozen when it was created — so an account that has since been
disabled, deleted or moved to a narrower role stops the work it armed. That is
what makes those three things kill switches instead of paperwork.
*/
func (db *DB) ActorByID(ctx context.Context, id uuid.UUID) (identity.Actor, error) {
	var (
		a           identity.Actor
		disabled    bool
		permissions []string
	)
	err := db.pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.display_name, u.role, u.disabled, r.permissions
		FROM users u
		JOIN roles r ON r.name = u.role
		WHERE u.id = $1`, id,
	).Scan(&a.UserID, &a.Email, &a.DisplayName, &a.Role, &disabled, &permissions)

	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Actor{}, identity.ErrUnauthenticated
	}
	if err != nil {
		return identity.Actor{}, fmt.Errorf("store: resolve actor: %w", err)
	}
	if disabled {
		return identity.Actor{}, identity.ErrUnauthenticated
	}
	a.Permissions = permissions
	return a, nil
}
