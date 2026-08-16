package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PluginSettings is one scope's worth of non-secret configuration.
type PluginSettings struct {
	Plugin     string         `json:"plugin"`
	CustomerID *uuid.UUID     `json:"customer_id,omitempty"`
	Values     map[string]any `json:"values"`
	UpdatedBy  *string        `json:"updated_by,omitempty"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// GetPluginConfig returns a scope's settings, or empty values when unset. A
// plugin that has never been configured is not an error — it simply has
// nothing yet, and its tools should report themselves unconfigured rather
// than failing obscurely.
func (db *DB) GetPluginConfig(ctx context.Context, plugin string, customerID *uuid.UUID) (PluginSettings, error) {
	s := PluginSettings{Plugin: plugin, CustomerID: customerID, Values: map[string]any{}}

	var raw []byte
	err := db.pool.QueryRow(ctx, `
		SELECT values, updated_by, updated_at
		FROM plugin_config
		WHERE plugin = $1 AND COALESCE(customer_id, $3::uuid) = COALESCE($2, $3::uuid)`,
		plugin, customerID, uuid.UUID{},
	).Scan(&raw, &s.UpdatedBy, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return PluginSettings{}, fmt.Errorf("store: get plugin config: %w", err)
	}
	if err := json.Unmarshal(raw, &s.Values); err != nil {
		return PluginSettings{}, fmt.Errorf("store: decode plugin config: %w", err)
	}
	return s, nil
}

// SetPluginConfig replaces a scope's settings.
//
// Callers must strip secret-marked fields before this point: they belong in
// the vault, not in a JSONB column. The API layer does that routing so a
// single admin form can hold both kinds of field.
func (db *DB) SetPluginConfig(ctx context.Context, plugin string, customerID *uuid.UUID, values map[string]any, by string) error {
	if plugin == "" {
		return errors.New("store: plugin is required")
	}
	if values == nil {
		values = map[string]any{}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return err
	}

	_, err = db.pool.Exec(ctx, `
		INSERT INTO plugin_config (plugin, customer_id, values, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (plugin, COALESCE(customer_id, '00000000-0000-0000-0000-000000000000'::uuid))
		DO UPDATE SET values = EXCLUDED.values,
		              updated_by = EXCLUDED.updated_by,
		              updated_at = now()`,
		plugin, customerID, encoded, by)
	if err != nil {
		return fmt.Errorf("store: set plugin config: %w", err)
	}
	return nil
}

// ListPluginConfig returns every scope configured for a plugin, so the console
// can show which customers have overrides.
func (db *DB) ListPluginConfig(ctx context.Context, plugin string) ([]PluginSettings, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT customer_id, values, updated_by, updated_at
		FROM plugin_config WHERE plugin = $1 ORDER BY customer_id NULLS FIRST`, plugin)
	if err != nil {
		return nil, fmt.Errorf("store: list plugin config: %w", err)
	}
	defer rows.Close()

	out := []PluginSettings{}
	for rows.Next() {
		s := PluginSettings{Plugin: plugin, Values: map[string]any{}}
		var raw []byte
		if err := rows.Scan(&s.CustomerID, &raw, &s.UpdatedBy, &s.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &s.Values); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
