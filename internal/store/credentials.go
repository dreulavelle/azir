package store

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/dreulavelle/azir/internal/vault"
)

// CredentialRef identifies a credential without carrying its value. Listing
// and administration work entirely in terms of these, so plaintext is only
// ever produced by an explicit Open.
type CredentialRef struct {
	ID         uuid.UUID  `json:"id"`
	CustomerID *uuid.UUID `json:"customer_id,omitempty"`
	Plugin     string     `json:"plugin"`
	Kind       string     `json:"kind"`
	KeyVersion int        `json:"key_version"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Redactor is told about every secret this package unseals, so that a value
// which later reaches a log line is scrubbed before it is written. Satisfied
// by *logging.Handler.
//
// An interface rather than the concrete handler because store must not depend
// on how logging is assembled, and because a test wants to see what was
// registered without a log sink to read back.
type Redactor interface {
	Register(values ...string)
}

// Credentials stores and retrieves sealed secrets. It is the only path to
// credential material, and it never returns plaintext except from Open.
type Credentials struct {
	db  *DB
	v   *vault.Vault
	red Redactor
}

// NewCredentials binds a vault to storage.
//
// red is a required argument rather than an optional setter: it is what keeps
// unsealed secrets out of the logs, and a deployment that forgot to attach one
// would look exactly like a deployment that had. Pass nil only in a test that
// is not asserting anything about redaction.
func NewCredentials(db *DB, v *vault.Vault, red Redactor) *Credentials {
	return &Credentials{db: db, v: v, red: red}
}

/*
credentialAAD is what a sealed credential is bound to: the scope it was stored
for, which is exactly the three things a lookup supplies to find it again.

Length-prefixed rather than joined with a separator, so the encoding is
injective. ("ab", "c") and ("a", "bc") would otherwise produce the same bytes,
and two distinct scopes sharing a binding is precisely the property this exists
to deny.
*/
func credentialAAD(plugin, kind string, scope uuid.UUID) []byte {
	aad := make([]byte, 0, 8+len(plugin)+len(kind)+len(scope))
	aad = binary.BigEndian.AppendUint32(aad, uint32(len(plugin)))
	aad = append(aad, plugin...)
	aad = binary.BigEndian.AppendUint32(aad, uint32(len(kind)))
	aad = append(aad, kind...)
	aad = append(aad, scope[:]...)
	return aad
}

// nilScope is the sentinel the unique index coalesces a NULL customer to, so
// a deployment-wide secret collides with itself instead of accumulating rows.
var nilScope = uuid.UUID{}

func scope(customerID *uuid.UUID) uuid.UUID {
	if customerID == nil {
		return nilScope
	}
	return *customerID
}

// Put seals and stores a secret, replacing any existing one for the same
// (plugin, kind, customer) scope.
func (c *Credentials) Put(ctx context.Context, customerID *uuid.UUID, plugin, kind string, secret []byte) (CredentialRef, error) {
	if plugin == "" || kind == "" {
		return CredentialRef{}, errors.New("store: plugin and kind are required")
	}
	if len(secret) == 0 {
		return CredentialRef{}, errors.New("store: refusing to store an empty secret")
	}

	sealed, err := c.v.Seal(secret, credentialAAD(plugin, kind, scope(customerID)))
	if err != nil {
		return CredentialRef{}, err
	}

	ref := CredentialRef{
		CustomerID: customerID,
		Plugin:     plugin,
		Kind:       kind,
		KeyVersion: sealed.KeyVersion,
	}

	// RETURNING gives back the surviving row, so an upsert reports the id that
	// is actually stored rather than the one this call proposed.
	err = c.db.pool.QueryRow(ctx, `
		INSERT INTO credentials
			(id, customer_id, plugin, kind, dek_wrapped, dek_nonce, ciphertext, nonce, key_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (plugin, kind, COALESCE(customer_id, $10::uuid)) DO UPDATE SET
			dek_wrapped = EXCLUDED.dek_wrapped,
			dek_nonce   = EXCLUDED.dek_nonce,
			ciphertext  = EXCLUDED.ciphertext,
			nonce       = EXCLUDED.nonce,
			key_version = EXCLUDED.key_version,
			updated_at  = now()
		RETURNING id, created_at, updated_at`,
		uuid.New(), customerID, plugin, kind,
		sealed.DEKWrapped, sealed.DEKNonce, sealed.Ciphertext, sealed.Nonce,
		sealed.KeyVersion, nilScope,
	).Scan(&ref.ID, &ref.CreatedAt, &ref.UpdatedAt)
	if err != nil {
		// The error must not echo any part of the secret.
		return CredentialRef{}, fmt.Errorf("store: put credential for plugin %q kind %q: %w", plugin, kind, err)
	}
	return ref, nil
}

// Open returns the plaintext secret for a scope. Callers must not log, format
// or return the result.
//
// Registration with the logging redactor happens here rather than in each
// caller. It was asked of callers once, in this comment, and not one of them
// did it — which is the ordinary fate of a step that has to be remembered.
// Doing it at the only place plaintext is produced means every secret is
// covered, including the ones unsealed by code written after this line.
func (c *Credentials) Open(ctx context.Context, customerID *uuid.UUID, plugin, kind string) ([]byte, error) {
	var s vault.Sealed
	err := c.db.pool.QueryRow(ctx, `
		SELECT dek_wrapped, dek_nonce, ciphertext, nonce, key_version
		FROM credentials
		WHERE plugin = $1 AND kind = $2 AND COALESCE(customer_id, $4::uuid) = $3`,
		plugin, kind, scope(customerID), nilScope,
	).Scan(&s.DEKWrapped, &s.DEKNonce, &s.Ciphertext, &s.Nonce, &s.KeyVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: load credential: %w", err)
	}
	secret, err := c.v.Open(s, credentialAAD(plugin, kind, scope(customerID)))
	if err != nil {
		return nil, err
	}
	if c.red != nil {
		c.red.Register(string(secret))
	}
	return secret, nil
}

// List returns references only. There is deliberately no way to enumerate
// secret values.
func (c *Credentials) List(ctx context.Context) ([]CredentialRef, error) {
	rows, err := c.db.pool.Query(ctx, `
		SELECT id, customer_id, plugin, kind, key_version, created_at, updated_at
		FROM credentials ORDER BY plugin, kind`)
	if err != nil {
		return nil, fmt.Errorf("store: list credentials: %w", err)
	}
	defer rows.Close()

	out := []CredentialRef{}
	for rows.Next() {
		var r CredentialRef
		if err := rows.Scan(&r.ID, &r.CustomerID, &r.Plugin, &r.Kind,
			&r.KeyVersion, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete removes a credential.
func (c *Credentials) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := c.db.pool.Exec(ctx, `DELETE FROM credentials WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete credential: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Rotate re-wraps every credential onto the current master key. Payloads are
// never decrypted, so rotation is cheap and the plaintext never materialises.
// It returns the number of credentials moved.
func (c *Credentials) Rotate(ctx context.Context) (int, error) {
	rows, err := c.db.pool.Query(ctx, `
		SELECT id, dek_wrapped, dek_nonce, ciphertext, nonce, key_version
		FROM credentials WHERE key_version <> $1`, c.v.CurrentVersion())
	if err != nil {
		return 0, fmt.Errorf("store: scan for rotation: %w", err)
	}

	type pending struct {
		id     uuid.UUID
		sealed vault.Sealed
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.sealed.DEKWrapped, &p.sealed.DEKNonce,
			&p.sealed.Ciphertext, &p.sealed.Nonce, &p.sealed.KeyVersion); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	moved := 0
	for _, p := range todo {
		rewrapped, err := c.v.Rewrap(p.sealed)
		if err != nil {
			return moved, fmt.Errorf("store: rewrap %s: %w", p.id, err)
		}
		if _, err := c.db.pool.Exec(ctx, `
			UPDATE credentials
			SET dek_wrapped = $2, dek_nonce = $3, key_version = $4, updated_at = now()
			WHERE id = $1`,
			p.id, rewrapped.DEKWrapped, rewrapped.DEKNonce, rewrapped.KeyVersion,
		); err != nil {
			return moved, fmt.Errorf("store: persist rewrap %s: %w", p.id, err)
		}
		moved++
	}
	return moved, nil
}
