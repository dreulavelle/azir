package store

import (
	"context"
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
	_, err := db.pool.Exec(ctx, `
		INSERT INTO capabilities (plugin, tool, status, provides)
		VALUES ($1, $2, 'pending', $3)
		ON CONFLICT (plugin, tool) DO UPDATE
		SET last_seen_at = now(), provides = EXCLUDED.provides`,
		plugin, tool, provides)
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
	tag, err := db.pool.Exec(ctx, `
		UPDATE capabilities
		SET status = $3, decided_by = $4, decided_at = now()
		WHERE plugin = $1 AND tool = $2`,
		plugin, tool, status, by)
	if err != nil {
		return fmt.Errorf("store: decide capability: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Capabilities returns every known tool decision.
func (db *DB) Capabilities(ctx context.Context) ([]CapabilityRecord, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT plugin, tool, status, provides, decided_by, decided_at, first_seen_at, last_seen_at
		FROM capabilities ORDER BY plugin, tool`)
	if err != nil {
		return nil, fmt.Errorf("store: list capabilities: %w", err)
	}
	defer rows.Close()

	out := []CapabilityRecord{}
	for rows.Next() {
		var r CapabilityRecord
		if err := rows.Scan(&r.Plugin, &r.Tool, &r.Status, &r.Provides,
			&r.DecidedBy, &r.DecidedAt, &r.FirstSeenAt, &r.LastSeenAt); err != nil {
			return nil, err
		}
		if r.Provides == nil {
			r.Provides = []string{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ApprovedTools returns the set of "plugin.tool" keys an administrator has
// activated. Everything else is invisible to the model.
func (db *DB) ApprovedTools(ctx context.Context) (map[string]struct{}, error) {
	rows, err := db.pool.Query(ctx,
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
