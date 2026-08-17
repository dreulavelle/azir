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

// Snapshot is one diagnostic capture, as stored.
//
// Report is the parsed result rather than the bundle. The bundle is not kept:
// it is enormous, it is the customer's, and everything worth having out of it
// is already a finding by the time this row exists.
type Snapshot struct {
	ID         uuid.UUID       `json:"id"`
	CustomerID uuid.UUID       `json:"customer_id"`
	Kind       string          `json:"kind"`
	Filename   string          `json:"filename,omitempty"`
	CapturedAt *time.Time      `json:"captured_at,omitempty"`
	Report     json.RawMessage `json:"report,omitempty"`
	Findings   int             `json:"findings"`
	Worst      string          `json:"worst,omitempty"`
	UploadedBy string          `json:"uploaded_by,omitempty"`
	UploadedAt time.Time       `json:"uploaded_at"`
}

// AddSnapshot stores one parsed capture.
func (db *DB) AddSnapshot(ctx context.Context, s Snapshot) (Snapshot, error) {
	if s.CustomerID == uuid.Nil {
		return Snapshot{}, errors.New("store: a snapshot belongs to a customer")
	}
	s.ID = uuid.New()
	if s.Kind == "" {
		s.Kind = "3cx-support-info"
	}
	err := db.pool.QueryRow(ctx, `
		INSERT INTO snapshots
			(id, customer_id, kind, filename, captured_at, report, findings, worst, uploaded_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING uploaded_at`,
		s.ID, s.CustomerID, s.Kind, s.Filename, s.CapturedAt, s.Report,
		s.Findings, s.Worst, s.UploadedBy,
	).Scan(&s.UploadedAt)
	if err != nil {
		return Snapshot{}, fmt.Errorf("store: add snapshot: %w", err)
	}
	return s, nil
}

// Snapshots lists a customer's captures, newest first, without their reports.
//
// The report is several hundred kilobytes and a list of twenty would be several
// megabytes of JSON to draw a list of dates.
func (db *DB) Snapshots(ctx context.Context, customer uuid.UUID) ([]Snapshot, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT id, customer_id, kind, filename, captured_at, findings, worst,
		       uploaded_by, uploaded_at
		FROM snapshots WHERE customer_id = $1
		ORDER BY captured_at DESC NULLS LAST, uploaded_at DESC`, customer)
	if err != nil {
		return nil, fmt.Errorf("store: snapshots: %w", err)
	}
	defer rows.Close()

	out := []Snapshot{}
	for rows.Next() {
		var s Snapshot
		if err := rows.Scan(&s.ID, &s.CustomerID, &s.Kind, &s.Filename, &s.CapturedAt,
			&s.Findings, &s.Worst, &s.UploadedBy, &s.UploadedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AllSnapshots lists every customer's captures, for the screen that opens on
// all of them rather than on one customer.
func (db *DB) AllSnapshots(ctx context.Context, limit int) ([]Snapshot, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.pool.Query(ctx, `
		SELECT id, customer_id, kind, filename, captured_at, findings, worst,
		       uploaded_by, uploaded_at
		FROM snapshots
		ORDER BY captured_at DESC NULLS LAST, uploaded_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: all snapshots: %w", err)
	}
	defer rows.Close()

	out := []Snapshot{}
	for rows.Next() {
		var s Snapshot
		if err := rows.Scan(&s.ID, &s.CustomerID, &s.Kind, &s.Filename, &s.CapturedAt,
			&s.Findings, &s.Worst, &s.UploadedBy, &s.UploadedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSnapshot returns one capture including its report.
func (db *DB) GetSnapshot(ctx context.Context, id uuid.UUID) (Snapshot, error) {
	var s Snapshot
	err := db.pool.QueryRow(ctx, `
		SELECT id, customer_id, kind, filename, captured_at, report, findings,
		       worst, uploaded_by, uploaded_at
		FROM snapshots WHERE id = $1`, id).
		Scan(&s.ID, &s.CustomerID, &s.Kind, &s.Filename, &s.CapturedAt, &s.Report,
			&s.Findings, &s.Worst, &s.UploadedBy, &s.UploadedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("store: get snapshot: %w", err)
	}
	return s, nil
}

// DeleteSnapshot removes one. These hold a customer's diagnostics and somebody
// should be able to get rid of one without a database session.
func (db *DB) DeleteSnapshot(ctx context.Context, id uuid.UUID) error {
	tag, err := db.pool.Exec(ctx, `DELETE FROM snapshots WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete snapshot: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
