package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AuthConfig is how this deployment talks to an identity provider.
//
// The client secret is deliberately absent: it lives in the credentials table
// under AuthPlugin, sealed like every other secret, so there is one answer to
// "where do secrets live" rather than two.
type AuthConfig struct {
	Enabled        bool      `json:"enabled"`
	Issuer         string    `json:"issuer"`
	ClientID       string    `json:"client_id"`
	TenantID       string    `json:"tenant_id"`
	AllowedDomains []string  `json:"allowed_domains"`
	AutoProvision  bool      `json:"auto_provision"`
	DefaultRole    string    `json:"default_role"`
	RedirectURL    string    `json:"redirect_url"`
	UpdatedAt      time.Time `json:"updated_at"`
	UpdatedBy      string    `json:"updated_by"`
}

// AuthPlugin is the pseudo-plugin the OIDC client secret is stored under, so it
// shares the vault, the rotation path and the audit trail with every other
// credential rather than getting a private arrangement.
const AuthPlugin = "azir.auth"

// AuthSecretKind is the credential kind for the OIDC client secret.
const AuthSecretKind = "client_secret"

// AuthConfig returns the single auth configuration row.
func (db *DB) AuthConfig(ctx context.Context) (AuthConfig, error) {
	var c AuthConfig
	err := db.pool.QueryRow(ctx, `
		SELECT enabled, issuer, client_id, tenant_id, allowed_domains,
		       auto_provision, default_role, redirect_url, updated_at, updated_by
		FROM auth_config WHERE id`).Scan(
		&c.Enabled, &c.Issuer, &c.ClientID, &c.TenantID, &c.AllowedDomains,
		&c.AutoProvision, &c.DefaultRole, &c.RedirectURL, &c.UpdatedAt, &c.UpdatedBy)
	if err != nil {
		return AuthConfig{}, fmt.Errorf("store: read auth config: %w", err)
	}
	return c, nil
}

// SetAuthConfig replaces the auth configuration.
func (db *DB) SetAuthConfig(ctx context.Context, c AuthConfig, actor string) error {
	domains := c.AllowedDomains
	if domains == nil {
		domains = []string{}
	}
	_, err := db.pool.Exec(ctx, `
		UPDATE auth_config SET
			enabled = $1, issuer = $2, client_id = $3, tenant_id = $4,
			allowed_domains = $5, auto_provision = $6, default_role = $7,
			redirect_url = $8, updated_at = now(), updated_by = $9
		WHERE id`,
		c.Enabled, c.Issuer, c.ClientID, c.TenantID, domains,
		c.AutoProvision, c.DefaultRole, c.RedirectURL, actor)
	if err != nil {
		if strings.Contains(err.Error(), "auth_config_default_role_fkey") {
			return fmt.Errorf("store: no such role %q", c.DefaultRole)
		}
		return fmt.Errorf("store: write auth config: %w", err)
	}
	return nil
}

// --- sign-in attempts --------------------------------------------------------

// OIDCState is what must survive the round trip to the provider unchanged.
type OIDCState struct {
	State    string
	Nonce    string
	Verifier string
}

// oidcStateLifetime bounds how long a sign-in may take. Long enough for
// someone to complete multi-factor authentication, short enough that an
// abandoned attempt is not left usable.
const oidcStateLifetime = 15 * time.Minute

// StartOIDCState records a sign-in attempt.
func (db *DB) StartOIDCState(ctx context.Context, s OIDCState) error {
	_, err := db.pool.Exec(ctx, `
		INSERT INTO oidc_states (state, nonce, verifier, expires_at)
		VALUES ($1, $2, $3, now() + $4::interval)`,
		s.State, s.Nonce, s.Verifier, oidcStateLifetime.String())
	if err != nil {
		return fmt.Errorf("store: start sign-in: %w", err)
	}
	return nil
}

// ErrNoSuchState means the callback presented a state nobody started, or one
// already used, or one that expired.
var ErrNoSuchState = errors.New("store: this sign-in attempt is not recognised")

// ConsumeOIDCState returns a sign-in attempt and deletes it.
//
// Deleting as part of the read is what makes an authorization code usable
// exactly once: a replayed callback finds nothing and is refused, rather than
// racing a second session into existence.
func (db *DB) ConsumeOIDCState(ctx context.Context, state string) (OIDCState, error) {
	var s OIDCState
	err := db.pool.QueryRow(ctx, `
		DELETE FROM oidc_states
		WHERE state = $1 AND expires_at > now()
		RETURNING state, nonce, verifier`, state).Scan(&s.State, &s.Nonce, &s.Verifier)
	if errors.Is(err, pgx.ErrNoRows) {
		return OIDCState{}, ErrNoSuchState
	}
	if err != nil {
		return OIDCState{}, fmt.Errorf("store: consume sign-in: %w", err)
	}
	return s, nil
}

// PurgeExpiredOIDCStates removes abandoned attempts.
func (db *DB) PurgeExpiredOIDCStates(ctx context.Context) (int64, error) {
	tag, err := db.pool.Exec(ctx, `DELETE FROM oidc_states WHERE expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("store: purge sign-in attempts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// --- federated accounts ------------------------------------------------------

// FederatedUser describes an account as the identity provider sees it.
type FederatedUser struct {
	Provider    string
	ExternalID  string
	Email       string
	DisplayName string
}

// ErrNotInvited means the provider authenticated someone this deployment has
// no account for, and auto-provisioning is off.
var ErrNotInvited = errors.New("store: no account exists for this person")

// LinkOrCreateFederatedUser resolves a provider identity to an Azir account.
//
// Matching is by the provider's stable identifier first and by email only as a
// fallback for an account that predates the provider being configured. Email is
// not a durable key — people are renamed — so once matched it is recorded
// against the stable one and never relied on again.
//
// The role is not taken from the token. Whoever this is, what they may do here
// is decided here.
func (db *DB) LinkOrCreateFederatedUser(ctx context.Context, f FederatedUser, autoProvision bool, defaultRole string) (User, error) {
	email := strings.TrimSpace(strings.ToLower(f.Email))
	if email == "" || f.ExternalID == "" {
		return User{}, errors.New("store: the provider returned no usable identity")
	}

	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("store: link account: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	const columns = `id, email, display_name, role, provider, disabled, created_at, last_seen_at`
	scan := func(row pgx.Row) (User, error) {
		var u User
		err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role,
			&u.Provider, &u.Disabled, &u.CreatedAt, &u.LastSeenAt)
		return u, err
	}

	// By stable identifier.
	u, err := scan(tx.QueryRow(ctx,
		`SELECT `+columns+` FROM users WHERE provider = $1 AND external_id = $2`,
		f.Provider, f.ExternalID))
	switch {
	case err == nil:
		// Keep the display name and address current; the directory is
		// authoritative for both.
		if _, err := tx.Exec(ctx,
			`UPDATE users SET email = $1, display_name = $2 WHERE id = $3`,
			email, f.DisplayName, u.ID); err != nil {
			return User{}, fmt.Errorf("store: refresh account: %w", err)
		}
		u.Email, u.DisplayName = email, f.DisplayName
		return u, tx.Commit(ctx)
	case !errors.Is(err, pgx.ErrNoRows):
		return User{}, fmt.Errorf("store: find account: %w", err)
	}

	// By email, for an account created before the provider was configured.
	u, err = scan(tx.QueryRow(ctx,
		`SELECT `+columns+` FROM users WHERE lower(email) = $1`, email))
	switch {
	case err == nil:
		if _, err := tx.Exec(ctx,
			`UPDATE users SET provider = $1, external_id = $2, display_name = $3 WHERE id = $4`,
			f.Provider, f.ExternalID, f.DisplayName, u.ID); err != nil {
			return User{}, fmt.Errorf("store: link account: %w", err)
		}
		u.Provider, u.DisplayName = &f.Provider, f.DisplayName
		return u, tx.Commit(ctx)
	case !errors.Is(err, pgx.ErrNoRows):
		return User{}, fmt.Errorf("store: find account: %w", err)
	}

	if !autoProvision {
		return User{}, ErrNotInvited
	}

	u = User{
		ID: uuid.New(), Email: email,
		DisplayName: strings.TrimSpace(f.DisplayName),
		Role:        defaultRole, Provider: &f.Provider,
	}
	// No password hash: this account has no local password to guess, and one
	// cannot be set for it without an administrator doing so deliberately.
	err = tx.QueryRow(ctx, `
		INSERT INTO users (id, email, display_name, role, provider, external_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at`,
		u.ID, u.Email, u.DisplayName, u.Role, f.Provider, f.ExternalID,
	).Scan(&u.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "users_role_fkey") {
			return User{}, fmt.Errorf("store: no such role %q", defaultRole)
		}
		return User{}, fmt.Errorf("store: create account: %w", err)
	}
	return u, tx.Commit(ctx)
}
