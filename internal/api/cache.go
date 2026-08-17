package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/registry"
	"github.com/dreulavelle/azir/internal/store"
)

// fetchFunc performs the real vendor call.
type fetchFunc func(ctx context.Context) (json.RawMessage, error)

// ToolCache serves tool results within a plugin-declared staleness budget.
//
// The shape is stale-while-revalidate with two thresholds. Below Soft a cached
// answer is returned as-is. Between Soft and Hard it is still returned
// immediately while a refresh runs behind it, so this caller is fast and the
// next one is current. Beyond Hard the caller waits.
//
// This is not a mirror of the vendor. Nothing is stored that nobody asked for,
// the vendor remains the source of truth, and every cached response carries the
// age of its data — serving something slightly stale is fine, implying it is
// live is not.
type ToolCache struct {
	db  *store.DB
	log *slog.Logger

	// inflight collapses concurrent refreshes of the same entry. Without it,
	// twenty callers arriving at an expired entry produce twenty vendor calls,
	// which is how a rate limit is discovered the hard way.
	mu       sync.Mutex
	inflight map[string]chan struct{}
}

// NewToolCache builds a cache.
func NewToolCache(db *store.DB, log *slog.Logger) *ToolCache {
	return &ToolCache{db: db, log: log, inflight: map[string]chan struct{}{}}
}

// Result is a tool result and how it was obtained.
type Result struct {
	Payload json.RawMessage
	// AgeSeconds is zero for a live call.
	AgeSeconds int
	// Source is "live", "cache" or "cache-refreshing".
	Source string
}

// Do returns a tool result, using the cache when the tool declares a freshness
// budget and refresh was not explicitly demanded.
func (c *ToolCache) Do(
	ctx context.Context,
	tool registry.Tool,
	customerID *uuid.UUID,
	args json.RawMessage,
	forceRefresh bool,
	fetch fetchFunc,
) (Result, error) {
	// No declared budget means the answer is expected to be live.
	if tool.Freshness == nil {
		payload, err := fetch(ctx)
		return Result{Payload: payload, Source: "live"}, err
	}

	key := store.ArgsKey(customerID, args)
	entryKey := tool.Plugin + "\x00" + tool.Name + "\x00" + key

	if !forceRefresh {
		if hit, err := c.db.GetCached(ctx, tool.Plugin, tool.Name, key); err == nil {
			age := hit.Age()
			switch {
			case age < tool.Freshness.Soft:
				return Result{Payload: hit.Payload, AgeSeconds: int(age.Seconds()), Source: "cache"}, nil

			case age < tool.Freshness.Hard:
				// Serve now, refresh behind. The caller gets a fast answer and
				// whoever asks next gets a current one.
				c.refreshInBackground(entryKey, tool, customerID, key, fetch)
				return Result{
					Payload:    hit.Payload,
					AgeSeconds: int(age.Seconds()),
					Source:     "cache-refreshing",
				}, nil
			}
		}
	}

	payload, err := fetch(ctx)
	if err != nil {
		// A vendor outage should not erase a usable answer. Serving something
		// stale and saying so beats serving nothing, so long as the age is
		// visible. A caller error is not an outage and gets no such kindness.
		if hit, cacheErr := c.db.GetCached(ctx, tool.Plugin, tool.Name, key); cacheErr == nil && !mistaken(err) {
			c.log.Warn("serving stale result after a vendor failure",
				"plugin", tool.Plugin, "tool", tool.Name, "age", hit.Age().String(), "error", err)
			return Result{
				Payload:    hit.Payload,
				AgeSeconds: int(hit.Age().Seconds()),
				Source:     "cache-stale-vendor-unavailable",
			}, nil
		}
		return Result{}, err
	}

	if err := c.db.PutCached(ctx, tool.Plugin, tool.Name, key, customerID, payload); err != nil {
		// A cache write failing must not fail the request.
		c.log.Warn("could not cache tool result", "plugin", tool.Plugin, "tool", tool.Name, "error", err)
	}
	return Result{Payload: payload, Source: "live"}, nil
}

// refreshInBackground updates one entry, at most once at a time.
func (c *ToolCache) refreshInBackground(
	entryKey string,
	tool registry.Tool,
	customerID *uuid.UUID,
	argsKey string,
	fetch fetchFunc,
) {
	c.mu.Lock()
	if _, running := c.inflight[entryKey]; running {
		c.mu.Unlock()
		return
	}
	done := make(chan struct{})
	c.inflight[entryKey] = done
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.inflight, entryKey)
			c.mu.Unlock()
			close(done)
		}()

		// Detached from the request: the caller has already been answered, and
		// a refresh should not be cancelled by them navigating away.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		payload, err := fetch(ctx)
		if err != nil {
			c.log.Warn("background refresh failed",
				"plugin", tool.Plugin, "tool", tool.Name, "error", err)
			return
		}
		if err := c.db.PutCached(ctx, tool.Plugin, tool.Name, argsKey, customerID, payload); err != nil {
			c.log.Warn("background refresh could not be cached",
				"plugin", tool.Plugin, "tool", tool.Name, "error", err)
		}
	}()
}

// Prune removes long-unused entries until ctx is cancelled. One-off questions
// nobody repeats would otherwise accumulate forever.
func (c *ToolCache) Prune(ctx context.Context, every, olderThan time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			removed, err := c.db.PruneCache(ctx, olderThan)
			if err != nil {
				c.log.Warn("cache prune failed", "error", err)
				continue
			}
			if removed > 0 {
				c.log.Info("pruned cached tool results", "removed", removed)
			}
		}
	}
}

// callerMistake marks a plugin error that was the caller's fault rather than a
// vendor's. Serving a stale answer over one of these would hide the mistake
// behind something that looks like a result.
type callerMistake struct{ code string }

func (e callerMistake) Error() string { return "the request was not valid: " + e.code }

// mistaken reports whether an error came from the caller getting it wrong.
func mistaken(err error) bool {
	var m callerMistake
	return errors.As(err, &m)
}
