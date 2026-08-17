package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// WebhookEndpoint is where a connected system can tell Azir something changed.
type WebhookEndpoint struct {
	Plugin     string     `json:"plugin"`
	Secret     string     `json:"secret"`
	LastSeen   *time.Time `json:"last_seen,omitempty"`
	Deliveries int64      `json:"deliveries"`
	Rejected   int64      `json:"rejected"`
	CreatedAt  time.Time  `json:"created_at"`
}

// WebhookEndpointFor returns the endpoint for a plugin, creating one on first
// ask.
//
// Created lazily rather than at install time so a deployment that never wires
// up a webhook never has a live URL sitting there waiting to be found.
func (db *DB) WebhookEndpointFor(ctx context.Context, plugin string) (WebhookEndpoint, error) {
	e, err := db.webhookEndpoint(ctx, plugin)
	if err == nil {
		return e, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return WebhookEndpoint{}, err
	}
	return db.RotateWebhookSecret(ctx, plugin)
}

func (db *DB) webhookEndpoint(ctx context.Context, plugin string) (WebhookEndpoint, error) {
	var e WebhookEndpoint
	err := db.pool.QueryRow(ctx, `
		SELECT plugin, secret, last_seen, deliveries, rejected, created_at
		FROM webhook_endpoints WHERE plugin = $1`, plugin).Scan(
		&e.Plugin, &e.Secret, &e.LastSeen, &e.Deliveries, &e.Rejected, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookEndpoint{}, ErrNotFound
	}
	if err != nil {
		return WebhookEndpoint{}, fmt.Errorf("store: read webhook endpoint: %w", err)
	}
	return e, nil
}

// RotateWebhookSecret issues a new URL for a plugin, revoking the old one.
func (db *DB) RotateWebhookSecret(ctx context.Context, plugin string) (WebhookEndpoint, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return WebhookEndpoint{}, fmt.Errorf("store: generate webhook secret: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)

	var e WebhookEndpoint
	err := db.pool.QueryRow(ctx, `
		INSERT INTO webhook_endpoints (plugin, secret) VALUES ($1, $2)
		ON CONFLICT (plugin) DO UPDATE
			SET secret = EXCLUDED.secret, deliveries = 0, rejected = 0, last_seen = NULL
		RETURNING plugin, secret, last_seen, deliveries, rejected, created_at`,
		plugin, secret).Scan(
		&e.Plugin, &e.Secret, &e.LastSeen, &e.Deliveries, &e.Rejected, &e.CreatedAt)
	if err != nil {
		return WebhookEndpoint{}, fmt.Errorf("store: rotate webhook secret: %w", err)
	}
	return e, nil
}

// WebhookBySecret resolves a delivery to the plugin it claims to come from.
//
// The lookup is the authentication, such as it is: an unguessable value in the
// path. Nothing downstream trusts the body regardless.
func (db *DB) WebhookBySecret(ctx context.Context, secret string) (WebhookEndpoint, error) {
	if secret == "" {
		return WebhookEndpoint{}, ErrNotFound
	}
	var e WebhookEndpoint
	err := db.pool.QueryRow(ctx, `
		SELECT plugin, secret, last_seen, deliveries, rejected, created_at
		FROM webhook_endpoints WHERE secret = $1`, secret).Scan(
		&e.Plugin, &e.Secret, &e.LastSeen, &e.Deliveries, &e.Rejected, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookEndpoint{}, ErrNotFound
	}
	if err != nil {
		return WebhookEndpoint{}, fmt.Errorf("store: resolve webhook: %w", err)
	}
	return e, nil
}

// RecordDelivery counts a delivery, accepted or not.
//
// Counted rather than logged in full: the body is somebody else's data and
// keeping every one of them would be a second copy of the ticket system with
// none of its access control.
func (db *DB) RecordDelivery(ctx context.Context, plugin string, accepted bool) error {
	column := "rejected"
	if accepted {
		column = "deliveries"
	}
	_, err := db.pool.Exec(ctx, fmt.Sprintf(`
		UPDATE webhook_endpoints
		SET %s = %s + 1, last_seen = now()
		WHERE plugin = $1`, column, column), plugin)
	if err != nil {
		return fmt.Errorf("store: record webhook delivery: %w", err)
	}
	return nil
}
