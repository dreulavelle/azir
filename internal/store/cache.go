package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CachedResult is a tool result with its age.
type CachedResult struct {
	Payload   json.RawMessage
	FetchedAt time.Time
}

// Age is how long ago this was fetched from the vendor.
func (c CachedResult) Age() time.Duration { return time.Since(c.FetchedAt) }

// ArgsKey builds a stable cache key from a tool's arguments and scope.
//
// Arguments are re-marshalled through a map so that key order, whitespace and
// formatting cannot produce two entries for the same question — a cache that
// misses on formatting is a cache that does nothing.
func ArgsKey(customerID *uuid.UUID, args json.RawMessage) string {
	canonical := []byte("{}")
	if len(args) > 0 {
		var decoded any
		if err := json.Unmarshal(args, &decoded); err == nil {
			if reencoded, err := json.Marshal(decoded); err == nil {
				canonical = reencoded
			}
		}
	}
	scope := ""
	if customerID != nil {
		scope = customerID.String()
	}
	sum := sha256.Sum256(append([]byte(scope+"\x00"), canonical...))
	return hex.EncodeToString(sum[:])
}

// GetCached returns a cached tool result, if one exists.
func (db *DB) GetCached(ctx context.Context, plugin, tool, argsKey string) (CachedResult, error) {
	var out CachedResult
	err := db.pool.QueryRow(ctx, `
		SELECT payload, fetched_at FROM tool_cache
		WHERE plugin = $1 AND tool = $2 AND args_key = $3`,
		plugin, tool, argsKey,
	).Scan(&out.Payload, &out.FetchedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return CachedResult{}, ErrNotFound
	}
	if err != nil {
		return CachedResult{}, fmt.Errorf("store: read tool cache: %w", err)
	}
	return out, nil
}

// PutCached stores a tool result.
func (db *DB) PutCached(ctx context.Context, plugin, tool, argsKey string, customerID *uuid.UUID, payload json.RawMessage) error {
	_, err := db.pool.Exec(ctx, `
		INSERT INTO tool_cache (plugin, tool, args_key, customer_id, payload, fetched_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (plugin, tool, args_key) DO UPDATE
		SET payload = EXCLUDED.payload,
		    customer_id = EXCLUDED.customer_id,
		    fetched_at = now()`,
		plugin, tool, argsKey, customerID, payload)
	if err != nil {
		return fmt.Errorf("store: write tool cache: %w", err)
	}
	return nil
}

// InvalidateCache drops cached entries. Passing a tool narrows it to that
// tool; passing a customer narrows it to that customer's data.
func (db *DB) InvalidateCache(ctx context.Context, plugin, tool string, customerID *uuid.UUID) (int64, error) {
	tag, err := db.pool.Exec(ctx, `
		DELETE FROM tool_cache
		WHERE plugin = $1
		  AND ($2 = '' OR tool = $2)
		  AND ($3::uuid IS NULL OR customer_id = $3)`,
		plugin, tool, customerID)
	if err != nil {
		return 0, fmt.Errorf("store: invalidate cache: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PruneCache removes entries older than the given age, so the table cannot
// grow without bound from one-off questions nobody asks again.
func (db *DB) PruneCache(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := db.pool.Exec(ctx,
		`DELETE FROM tool_cache WHERE fetched_at < now() - $1::interval`,
		olderThan.String())
	if err != nil {
		return 0, fmt.Errorf("store: prune cache: %w", err)
	}
	return tag.RowsAffected(), nil
}
