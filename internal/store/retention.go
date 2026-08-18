package store

import (
	"context"
	"fmt"
	"time"
)

/*
How long the two things that expire are kept.

Both were constants compiled into the binary, and both are shown on the Data
screen as a promise about somebody's customers' data. An MSP that has agreed to
hold diagnostic captures for thirty days, or for seven, could keep that promise
only by rebuilding Azir.

Read at the point of use rather than cached in memory. These are consulted when
a capture is stored and once an hour by a sweep — never in a loop, never on a
request path that matters — so a query is cheaper than the bug where somebody
changes the number and nothing acts on it until the next restart.
*/

// Retention is how long each kind of thing is kept, in days.
type Retention struct {
	// CaptureDays is how long a diagnostic capture lasts unless pinned.
	CaptureDays int `json:"capture_days"`
	// CacheDays is how long a cached tool result may be reused.
	CacheDays int `json:"cache_days"`
}

// Bounds on what may be set, matched to the constraints in the migration so a
// bad value is refused with a sentence rather than a database error.
const (
	MinCaptureDays, MaxCaptureDays = 1, 365
	MinCacheDays, MaxCacheDays     = 1, 90
)

// GetRetention reads the settings.
func (db *DB) GetRetention(ctx context.Context) (Retention, error) {
	var r Retention
	err := db.pool.QueryRow(ctx,
		`SELECT capture_days, cache_days FROM retention WHERE id`).
		Scan(&r.CaptureDays, &r.CacheDays)
	if err != nil {
		return Retention{}, fmt.Errorf("store: retention: %w", err)
	}
	return r, nil
}

// SetRetention saves them.
func (db *DB) SetRetention(ctx context.Context, r Retention) error {
	if r.CaptureDays < MinCaptureDays || r.CaptureDays > MaxCaptureDays {
		return fmt.Errorf("store: captures must be kept between %d and %d days",
			MinCaptureDays, MaxCaptureDays)
	}
	if r.CacheDays < MinCacheDays || r.CacheDays > MaxCacheDays {
		return fmt.Errorf("store: cached answers must be kept between %d and %d days",
			MinCacheDays, MaxCacheDays)
	}
	_, err := db.pool.Exec(ctx,
		`UPDATE retention SET capture_days = $1, cache_days = $2, updated_at = now() WHERE id`,
		r.CaptureDays, r.CacheDays)
	if err != nil {
		return fmt.Errorf("store: save retention: %w", err)
	}
	return nil
}

// CaptureLife is how long a capture is kept, as a duration.
//
// Falls back to the shipped default if the setting cannot be read, because
// refusing to store a capture over a failed settings query would be a worse
// answer than storing it with the fortnight everybody had before.
func (db *DB) CaptureLife(ctx context.Context) time.Duration {
	r, err := db.GetRetention(ctx)
	if err != nil || r.CaptureDays <= 0 {
		return SnapshotLife
	}
	return time.Duration(r.CaptureDays) * 24 * time.Hour
}
