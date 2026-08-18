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

/*
Sheets somebody uploaded, between the upload and the decision.

Held in the database rather than in a browser tab because those are separate
visits: a file is uploaded, the difference from what the phone system currently
says is worked out, and somebody looks at it before deciding. A refresh in the
middle of that should not lose the whole thing.

It holds a customer's extension numbers and the names on them, so it expires
the way a diagnostic capture does. The point of the file was the change it
described — once that is applied or declined, keeping a copy of somebody's
phone directory serves nobody.
*/

// BulkEdit is one uploaded sheet and what became of it.
type BulkEdit struct {
	ID         uuid.UUID       `json:"id"`
	CustomerID uuid.UUID       `json:"customer_id"`
	Filename   string          `json:"filename"`
	UploadedBy string          `json:"uploaded_by"`
	Sheet      json.RawMessage `json:"sheet"`
	Mapping    json.RawMessage `json:"mapping,omitempty"`
	Plan       json.RawMessage `json:"plan,omitempty"`
	Outcome    json.RawMessage `json:"outcome,omitempty"`
	Status     string          `json:"status"`
	CreatedAt  time.Time       `json:"created_at"`
	DecidedAt  *time.Time      `json:"decided_at,omitempty"`
	DecidedBy  string          `json:"decided_by,omitempty"`
}

const bulkColumns = `id, customer_id, filename, uploaded_by, sheet, mapping,
	plan, outcome, status, created_at, decided_at, decided_by`

func scanBulk(row pgx.Row) (BulkEdit, error) {
	var b BulkEdit
	err := row.Scan(&b.ID, &b.CustomerID, &b.Filename, &b.UploadedBy, &b.Sheet,
		&b.Mapping, &b.Plan, &b.Outcome, &b.Status, &b.CreatedAt, &b.DecidedAt, &b.DecidedBy)
	return b, err
}

// AddBulkEdit stores a parsed sheet awaiting a mapping.
func (db *DB) AddBulkEdit(ctx context.Context, b BulkEdit) (BulkEdit, error) {
	b.ID = uuid.New()
	expires := time.Now().Add(db.CaptureLife(ctx))
	row := db.pool.QueryRow(ctx, `
		INSERT INTO bulk_edits (id, customer_id, filename, uploaded_by, sheet, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+bulkColumns,
		b.ID, b.CustomerID, b.Filename, b.UploadedBy, b.Sheet, expires)
	out, err := scanBulk(row)
	if err != nil {
		return BulkEdit{}, fmt.Errorf("store: add bulk edit: %w", err)
	}
	return out, nil
}

// BulkEditByID reads one.
func (db *DB) BulkEditByID(ctx context.Context, id uuid.UUID) (BulkEdit, error) {
	row := db.pool.QueryRow(ctx, `SELECT `+bulkColumns+` FROM bulk_edits WHERE id = $1`, id)
	out, err := scanBulk(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return BulkEdit{}, ErrNotFound
	}
	if err != nil {
		return BulkEdit{}, fmt.Errorf("store: bulk edit: %w", err)
	}
	return out, nil
}

// SaveBulkPlan records the mapping and the before-and-after it produced.
func (db *DB) SaveBulkPlan(ctx context.Context, id uuid.UUID, mapping, plan json.RawMessage) (BulkEdit, error) {
	row := db.pool.QueryRow(ctx, `
		UPDATE bulk_edits SET mapping = $2, plan = $3, status = 'planned'
		WHERE id = $1 AND status IN ('draft', 'planned')
		RETURNING `+bulkColumns, id, mapping, plan)
	out, err := scanBulk(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either it is gone, or it has already been decided — and re-planning
		// something that was applied would quietly offer to apply it twice.
		return BulkEdit{}, ErrNotFound
	}
	if err != nil {
		return BulkEdit{}, fmt.Errorf("store: save bulk plan: %w", err)
	}
	return out, nil
}

/*
FinishBulkEdit records what happened, once and only once.

The status is part of the condition rather than checked beforehand. Two clicks
on Apply, or a retried request, would otherwise both find a planned edit and
both go through — and applying a sheet twice to somebody's phone system is the
kind of mistake that is invisible until a technician wonders why their change
was reverted and reapplied.
*/
func (db *DB) FinishBulkEdit(ctx context.Context, id uuid.UUID, status, by string, outcome json.RawMessage) (BulkEdit, error) {
	row := db.pool.QueryRow(ctx, `
		UPDATE bulk_edits
		   SET status = $2, decided_by = $3, decided_at = now(), outcome = $4
		 WHERE id = $1 AND status = 'planned'
		RETURNING `+bulkColumns, id, status, by, outcome)
	out, err := scanBulk(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return BulkEdit{}, ErrNotFound
	}
	if err != nil {
		return BulkEdit{}, fmt.Errorf("store: finish bulk edit: %w", err)
	}
	return out, nil
}

// ListBulkEdits returns recent ones for a customer.
func (db *DB) ListBulkEdits(ctx context.Context, customerID uuid.UUID, limit int) ([]BulkEdit, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := db.pool.Query(ctx,
		`SELECT `+bulkColumns+` FROM bulk_edits WHERE customer_id = $1
		  ORDER BY created_at DESC LIMIT $2`, customerID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list bulk edits: %w", err)
	}
	defer rows.Close()

	out := []BulkEdit{}
	for rows.Next() {
		b, err := scanBulk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SweepBulkEdits removes the ones whose time is up.
func (db *DB) SweepBulkEdits(ctx context.Context) (int64, error) {
	tag, err := db.pool.Exec(ctx,
		`DELETE FROM bulk_edits WHERE expires_at IS NOT NULL AND expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("store: sweep bulk edits: %w", err)
	}
	return tag.RowsAffected(), nil
}
