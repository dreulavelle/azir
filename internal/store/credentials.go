package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

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

// Credentials stores and retrieves sealed secrets. It is the only path to
// credential material, and it never returns plaintext except from Open.
type Credentials struct {
	db *DB
	v  *vault.Vault
}

// NewCredentials binds a vault to storage.
func NewCredentials(db *DB, v *vault.Vault) *Credentials {
	return &Credentials{db: db, v: v}
}

// scopeKey renders the nullable customer scope the same way the unique index
// does, so a deployment-wide secret collides with itself rather than
// accumulating duplicate rows.
func scopeKey(customerID *uuid.UUID) string {
	if customerID == nil {
		return ""
	}
	return customerID.String()
}

func scopeArg(customerID *uuid.UUID) any {
	if customerID == nil {
		return nil
	}
	return customerID.String()
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

	sealed, err := c.v.Seal(secret)
	if err != nil {
		return CredentialRef{}, err
	}

	now := nowString()
	ref := CredentialRef{
		ID:         uuid.New(),
		CustomerID: customerID,
		Plugin:     plugin,
		Kind:       kind,
		KeyVersion: sealed.KeyVersion,
		CreatedAt:  parseTime(now),
		UpdatedAt:  parseTime(now),
	}

	_, err = c.db.write.ExecContext(ctx, `
		INSERT INTO credentials
			(id, customer_id, plugin, kind, dek_wrapped, dek_nonce, ciphertext, nonce,
			 key_version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (plugin, kind, COALESCE(customer_id, '')) DO UPDATE SET
			dek_wrapped = excluded.dek_wrapped,
			dek_nonce   = excluded.dek_nonce,
			ciphertext  = excluded.ciphertext,
			nonce       = excluded.nonce,
			key_version = excluded.key_version,
			updated_at  = excluded.updated_at`,
		ref.ID.String(), scopeArg(customerID), plugin, kind,
		sealed.DEKWrapped, sealed.DEKNonce, sealed.Ciphertext, sealed.Nonce,
		sealed.KeyVersion, now, now)
	if err != nil {
		// The error must not echo any part of the secret.
		return CredentialRef{}, fmt.Errorf("store: put credential for plugin %q kind %q: %w", plugin, kind, err)
	}

	// An upsert keeps the original id; report what is actually stored.
	var stored string
	if err := c.db.read.QueryRowContext(ctx,
		`SELECT id FROM credentials WHERE plugin = ? AND kind = ? AND COALESCE(customer_id, '') = ?`,
		plugin, kind, scopeKey(customerID)).Scan(&stored); err == nil {
		if parsed, err := uuid.Parse(stored); err == nil {
			ref.ID = parsed
		}
	}
	return ref, nil
}

// Open returns the plaintext secret for a scope. Callers must not log, format
// or return the result; register it with the logging redactor instead.
func (c *Credentials) Open(ctx context.Context, customerID *uuid.UUID, plugin, kind string) ([]byte, error) {
	var s vault.Sealed
	err := c.db.read.QueryRowContext(ctx, `
		SELECT dek_wrapped, dek_nonce, ciphertext, nonce, key_version
		FROM credentials
		WHERE plugin = ? AND kind = ? AND COALESCE(customer_id, '') = ?`,
		plugin, kind, scopeKey(customerID),
	).Scan(&s.DEKWrapped, &s.DEKNonce, &s.Ciphertext, &s.Nonce, &s.KeyVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: load credential: %w", err)
	}
	return c.v.Open(s)
}

// List returns references only. There is deliberately no way to enumerate
// secret values.
func (c *Credentials) List(ctx context.Context) ([]CredentialRef, error) {
	rows, err := c.db.read.QueryContext(ctx, `
		SELECT id, customer_id, plugin, kind, key_version, created_at, updated_at
		FROM credentials ORDER BY plugin, kind`)
	if err != nil {
		return nil, fmt.Errorf("store: list credentials: %w", err)
	}
	defer rows.Close()

	out := []CredentialRef{}
	for rows.Next() {
		var (
			r                    CredentialRef
			idStr                string
			customerID           sql.NullString
			createdAt, updatedAt string
		)
		if err := rows.Scan(&idStr, &customerID, &r.Plugin, &r.Kind,
			&r.KeyVersion, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		r.ID, _ = uuid.Parse(idStr)
		if customerID.Valid && customerID.String != "" {
			if parsed, err := uuid.Parse(customerID.String); err == nil {
				r.CustomerID = &parsed
			}
		}
		r.CreatedAt, r.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete removes a credential.
func (c *Credentials) Delete(ctx context.Context, id uuid.UUID) error {
	res, err := c.db.write.ExecContext(ctx, `DELETE FROM credentials WHERE id = ?`, id.String())
	if err != nil {
		return fmt.Errorf("store: delete credential: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Rotate re-wraps every credential onto the current master key. Payloads are
// never decrypted, so rotation is cheap and the plaintext never materialises.
// It returns the number of credentials moved.
func (c *Credentials) Rotate(ctx context.Context) (int, error) {
	rows, err := c.db.read.QueryContext(ctx, `
		SELECT id, dek_wrapped, dek_nonce, ciphertext, nonce, key_version
		FROM credentials WHERE key_version <> ?`, c.v.CurrentVersion())
	if err != nil {
		return 0, fmt.Errorf("store: scan for rotation: %w", err)
	}

	type pending struct {
		id     string
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
		if _, err := c.db.write.ExecContext(ctx, `
			UPDATE credentials
			SET dek_wrapped = ?, dek_nonce = ?, key_version = ?, updated_at = ?
			WHERE id = ?`,
			rewrapped.DEKWrapped, rewrapped.DEKNonce, rewrapped.KeyVersion, nowString(), p.id,
		); err != nil {
			return moved, fmt.Errorf("store: persist rewrap %s: %w", p.id, err)
		}
		moved++
	}
	return moved, nil
}
