package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Capability approval states.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

// CapabilityRecord is the administrator's decision about one discovered tool.
type CapabilityRecord struct {
	Plugin      string     `json:"plugin"`
	Tool        string     `json:"tool"`
	Status      string     `json:"status"`
	Provides    []string   `json:"provides"`
	DecidedBy   *string    `json:"decided_by,omitempty"`
	DecidedAt   *time.Time `json:"decided_at,omitempty"`
	FirstSeenAt time.Time  `json:"first_seen_at"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
}

// Observe records that a tool was seen in discovery. A tool appearing for the
// first time lands as pending and stays unusable until approved — discovery
// proposes, an administrator disposes. An existing decision is never
// overwritten by rediscovery, so restarting a plugin cannot launder a
// rejection into a fresh pending state.
func (db *DB) Observe(ctx context.Context, plugin, tool string, provides []string) error {
	if provides == nil {
		provides = []string{}
	}
	encoded, err := json.Marshal(provides)
	if err != nil {
		return err
	}
	now := nowString()
	_, err = db.write.ExecContext(ctx, `
		INSERT INTO capabilities (plugin, tool, status, provides, first_seen_at, last_seen_at)
		VALUES (?, ?, 'pending', ?, ?, ?)
		ON CONFLICT (plugin, tool) DO UPDATE
		SET last_seen_at = excluded.last_seen_at, provides = excluded.provides`,
		plugin, tool, string(encoded), now, now)
	if err != nil {
		return fmt.Errorf("store: observe capability %s.%s: %w", plugin, tool, err)
	}
	return nil
}

// Decide approves or rejects a tool.
func (db *DB) Decide(ctx context.Context, plugin, tool, status, by string) error {
	if status != StatusApproved && status != StatusRejected && status != StatusPending {
		return fmt.Errorf("store: invalid capability status %q", status)
	}
	res, err := db.write.ExecContext(ctx, `
		UPDATE capabilities
		SET status = ?, decided_by = ?, decided_at = ?
		WHERE plugin = ? AND tool = ?`,
		status, by, nowString(), plugin, tool)
	if err != nil {
		return fmt.Errorf("store: decide capability: %w", err)
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

// Capabilities returns every known tool decision.
func (db *DB) Capabilities(ctx context.Context) ([]CapabilityRecord, error) {
	rows, err := db.read.QueryContext(ctx, `
		SELECT plugin, tool, status, provides, decided_by, decided_at, first_seen_at, last_seen_at
		FROM capabilities ORDER BY plugin, tool`)
	if err != nil {
		return nil, fmt.Errorf("store: list capabilities: %w", err)
	}
	defer rows.Close()

	out := []CapabilityRecord{}
	for rows.Next() {
		var (
			r                       CapabilityRecord
			provides                string
			decidedBy, decidedAt    sql.NullString
			firstSeenAt, lastSeenAt string
		)
		if err := rows.Scan(&r.Plugin, &r.Tool, &r.Status, &provides,
			&decidedBy, &decidedAt, &firstSeenAt, &lastSeenAt); err != nil {
			return nil, err
		}
		r.Provides = []string{}
		_ = json.Unmarshal([]byte(provides), &r.Provides)
		if decidedBy.Valid {
			v := decidedBy.String
			r.DecidedBy = &v
		}
		if decidedAt.Valid {
			t := parseTime(decidedAt.String)
			r.DecidedAt = &t
		}
		r.FirstSeenAt, r.LastSeenAt = parseTime(firstSeenAt), parseTime(lastSeenAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ApprovedTools returns the set of "plugin.tool" keys an administrator has
// activated. Everything else is invisible to the model.
func (db *DB) ApprovedTools(ctx context.Context) (map[string]struct{}, error) {
	rows, err := db.read.QueryContext(ctx,
		`SELECT plugin, tool FROM capabilities WHERE status = 'approved'`)
	if err != nil {
		return nil, fmt.Errorf("store: approved tools: %w", err)
	}
	defer rows.Close()

	out := map[string]struct{}{}
	for rows.Next() {
		var plugin, tool string
		if err := rows.Scan(&plugin, &tool); err != nil {
			return nil, err
		}
		out[plugin+"."+tool] = struct{}{}
	}
	return out, rows.Err()
}
