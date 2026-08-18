package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

/*
Open sessions, and ending them.

Every sign-in already recorded the browser and the address it came from, and
nothing had ever read either. What that means in practice is that an MSP
holding its customers' data could not answer the first question anybody asks
after a laptop goes missing — where is this account signed in, and can I stop
it — without a database client.

A session is identified by the hash of its token. The hash is what the table is
keyed on, it is not a credential, and it cannot be turned back into the token
that would be one, so it is safe to name in a URL. Adding a second identifier
alongside it would be one more column to keep in step for no gain.
*/

// Session is one open sign-in.
type Session struct {
	// ID is the token's hash. Safe to show; useless to steal.
	ID        string    `json:"id"`
	UserID    uuid.UUID `json:"user_id"`
	Email     string    `json:"email"`
	StartedAt time.Time `json:"started_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Device is what the browser called itself, as sent.
	Device string `json:"device,omitempty"`
	IP     string `json:"ip,omitempty"`
	// Current marks the session making the request, so nobody signs
	// themselves out wondering why the page stopped working.
	Current bool `json:"current"`
}

// ListSessions returns every session that has not expired, newest first.
func (db *DB) ListSessions(ctx context.Context, current string) ([]Session, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT s.token_hash, s.user_id, u.email, s.created_at, s.expires_at,
		       coalesce(s.user_agent, ''), coalesce(s.ip, '')
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.expires_at > now()
		ORDER BY s.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	defer rows.Close()

	out := []Session{}
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.UserID, &s.Email, &s.StartedAt,
			&s.ExpiresAt, &s.Device, &s.IP); err != nil {
			return nil, err
		}
		s.Current = s.ID == current
		out = append(out, s)
	}
	return out, rows.Err()
}

// EndSession signs out one session by its hash.
func (db *DB) EndSession(ctx context.Context, id string) error {
	tag, err := db.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, id)
	if err != nil {
		return fmt.Errorf("store: end session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
