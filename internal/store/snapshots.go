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
	ID uuid.UUID `json:"id"`
	/*
		CustomerID is uuid.Nil on a capture nobody has attached yet.

		A bundle arrives before anybody has decided whose it is, and refusing to
		read one until a customer exists would make the tool useless exactly
		when it is most wanted.

		Serialised through Customer below rather than directly, because
		uuid.UUID is an array and `omitempty` does nothing to one: an
		unattached capture would go over the wire as a customer id of all
		zeroes, which reads to everything downstream as a customer that exists.
		The screen that offers to attach it would never appear.
	*/
	CustomerID uuid.UUID `json:"-"`
	// Customer is the wire form: absent when nobody owns this yet.
	Customer *uuid.UUID `json:"customer_id,omitempty"`
	// FQDN is the phone system's own address, read out of the bundle. It is
	// what attached this to a customer, and on one that could not be attached
	// it is the clue to who it belongs to.
	FQDN       string          `json:"fqdn,omitempty"`
	Kind       string          `json:"kind"`
	Filename   string          `json:"filename,omitempty"`
	CapturedAt *time.Time      `json:"captured_at,omitempty"`
	Report     json.RawMessage `json:"report,omitempty"`
	Findings   int             `json:"findings"`
	Worst      string          `json:"worst,omitempty"`
	UploadedBy string          `json:"uploaded_by,omitempty"`
	UploadedAt time.Time       `json:"uploaded_at"`
	// ExpiresAt is when this is deleted. Nil means somebody pinned it.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// SnapshotLife is how long a capture is kept unless somebody pins it, when
// nothing has said otherwise.
//
// A fortnight covers the ticket that prompted it and the follow-up, and is
// short enough that a customer's extension numbers, MAC addresses and
// administrators' names are not sitting in the database a year later to answer
// a question nobody is still asking. It is the default rather than the rule:
// see CaptureLife, which is what the sweep and the two writes below use.
const SnapshotLife = 14 * 24 * time.Hour

// AddSnapshot stores one parsed capture.
func (db *DB) AddSnapshot(ctx context.Context, s Snapshot) (Snapshot, error) {
	s.ID = uuid.New()
	if s.Kind == "" {
		s.Kind = "3cx-support-info"
	}
	if s.ExpiresAt == nil {
		expires := time.Now().Add(db.CaptureLife(ctx))
		s.ExpiresAt = &expires
	}
	err := db.pool.QueryRow(ctx, `
		INSERT INTO snapshots
			(id, customer_id, fqdn, kind, filename, captured_at, report, findings,
			 worst, uploaded_by, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING uploaded_at`,
		s.ID, nilUUID(s.CustomerID), s.FQDN, s.Kind, s.Filename, s.CapturedAt, s.Report,
		s.Findings, s.Worst, s.UploadedBy, s.ExpiresAt,
	).Scan(&s.UploadedAt)
	if err != nil {
		return Snapshot{}, fmt.Errorf("store: add snapshot: %w", err)
	}
	s.Customer = nilUUID(s.CustomerID)
	return s, nil
}

// Snapshots lists a customer's captures, newest first, without their reports.
//
// The report is several hundred kilobytes and a list of twenty would be several
// megabytes of JSON to draw a list of dates.
func (db *DB) Snapshots(ctx context.Context, customer uuid.UUID) ([]Snapshot, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT id, COALESCE(customer_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       fqdn, kind, filename, captured_at, findings, worst,
		       uploaded_by, uploaded_at, expires_at
		FROM snapshots WHERE customer_id = $1
		ORDER BY captured_at DESC NULLS LAST, uploaded_at DESC`, customer)
	if err != nil {
		return nil, fmt.Errorf("store: snapshots: %w", err)
	}
	defer rows.Close()

	out := []Snapshot{}
	for rows.Next() {
		var s Snapshot
		if err := rows.Scan(&s.ID, &s.CustomerID, &s.FQDN, &s.Kind, &s.Filename, &s.CapturedAt,
			&s.Findings, &s.Worst, &s.UploadedBy, &s.UploadedAt, &s.ExpiresAt); err != nil {
			return nil, err
		}
		s.Customer = nilUUID(s.CustomerID)
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
		SELECT id, COALESCE(customer_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       fqdn, kind, filename, captured_at, findings, worst,
		       uploaded_by, uploaded_at, expires_at
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
		if err := rows.Scan(&s.ID, &s.CustomerID, &s.FQDN, &s.Kind, &s.Filename, &s.CapturedAt,
			&s.Findings, &s.Worst, &s.UploadedBy, &s.UploadedAt, &s.ExpiresAt); err != nil {
			return nil, err
		}
		s.Customer = nilUUID(s.CustomerID)
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSnapshot returns one capture including its report.
func (db *DB) GetSnapshot(ctx context.Context, id uuid.UUID) (Snapshot, error) {
	var s Snapshot
	err := db.pool.QueryRow(ctx, `
		SELECT id, COALESCE(customer_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       fqdn, kind, filename, captured_at, report, findings,
		       worst, uploaded_by, uploaded_at, expires_at
		FROM snapshots WHERE id = $1`, id).
		Scan(&s.ID, &s.CustomerID, &s.FQDN, &s.Kind, &s.Filename, &s.CapturedAt, &s.Report,
			&s.Findings, &s.Worst, &s.UploadedBy, &s.UploadedAt, &s.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("store: get snapshot: %w", err)
	}
	s.Customer = nilUUID(s.CustomerID)
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

// nilUUID turns Azir's "no customer" into the database's.
//
// uuid.Nil is a real value made of zeroes and the column has a foreign key, so
// storing it would fail on a customer that cannot exist. NULL is what "nobody
// yet" means here.
func nilUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

/*
CustomerByPluginValue finds the customer a plugin setting points at.

This is how an uploaded bundle attaches itself. A 3CX capture carries the phone
system's own FQDN, and the FQDN is exactly what an administrator typed into
that customer's 3CX settings for Azir to reach the PBX at — so the join already
exists and nobody has to be asked which customer a zip belongs to.

Returns uuid.Nil rather than an error when nothing matches, because not
recognising a bundle is an ordinary outcome and not a failure.

It also returns nothing when more than one customer claims the same address,
which happens more than it sounds: a trial PBX gets pointed at several customer
records while somebody is setting Azir up, and two sites genuinely can share a
hosted instance. Picking one of them arbitrarily would file a customer's
extension numbers, call records and administrators' names under another
customer's name — silently, and with nothing on the page to suggest it was a
guess. Leaving it unattached asks a question instead, which is the honest
answer to an ambiguous one.
*/
func (db *DB) CustomerByPluginValue(ctx context.Context, plugin, field, value string) (uuid.UUID, error) {
	if value == "" {
		return uuid.Nil, nil
	}
	rows, err := db.pool.Query(ctx, `
		SELECT DISTINCT customer_id FROM plugin_config
		WHERE plugin = $1
		  AND customer_id IS NOT NULL
		  AND lower(values->>$2) = lower($3)
		LIMIT 2`, plugin, field, value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("store: customer by %s: %w", field, err)
	}
	defer rows.Close()

	var found []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return uuid.Nil, err
		}
		found = append(found, id)
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, fmt.Errorf("store: customer by %s: %w", field, err)
	}
	if len(found) != 1 {
		return uuid.Nil, nil
	}
	return found[0], nil
}

// AttachSnapshot links a capture to a customer, or unlinks it.
func (db *DB) AttachSnapshot(ctx context.Context, id, customer uuid.UUID) error {
	tag, err := db.pool.Exec(ctx,
		`UPDATE snapshots SET customer_id = $2 WHERE id = $1`, id, nilUUID(customer))
	if err != nil {
		return fmt.Errorf("store: attach snapshot: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

/*
KeepSnapshot pins a capture, or hands it back to the expiry sweep.

Pinning is what stops the fortnight running out on the one capture that turned
out to explain something. It is deliberately a decision somebody makes rather
than a default, because the default has to be that diagnostic data goes away.
*/
func (db *DB) KeepSnapshot(ctx context.Context, id uuid.UUID, keep bool) error {
	var expires *time.Time
	if !keep {
		when := time.Now().Add(db.CaptureLife(ctx))
		expires = &when
	}
	tag, err := db.pool.Exec(ctx,
		`UPDATE snapshots SET expires_at = $2 WHERE id = $1`, id, expires)
	if err != nil {
		return fmt.Errorf("store: keep snapshot: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SweepSnapshots deletes the captures whose time is up, and reports how many.
func (db *DB) SweepSnapshots(ctx context.Context) (int64, error) {
	tag, err := db.pool.Exec(ctx,
		`DELETE FROM snapshots WHERE expires_at IS NOT NULL AND expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("store: sweep snapshots: %w", err)
	}
	return tag.RowsAffected(), nil
}
