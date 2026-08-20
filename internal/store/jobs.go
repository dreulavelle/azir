package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

/*
Work somebody asked for, to happen later.

The row is the intent and the record; the timer lives in NATS. Everything a
person needs to answer — what is armed, who armed it, did it fire, what did the
far end say — is answered from here, because a message sitting in a stream
waiting for its moment can answer none of it.
*/

// Job states. A one-shot ends at done, failed or cancelled; a repeating job
// returns to scheduled after every run and only leaves on cancellation.
const (
	JobScheduled = "scheduled"
	JobRunning   = "running"
	JobDone      = "done"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// Job is one deferred tool call.
type Job struct {
	ID         uuid.UUID       `json:"id"`
	CustomerID *uuid.UUID      `json:"customer_id,omitempty"`
	Plugin     string          `json:"plugin"`
	Tool       string          `json:"tool"`
	Args       json.RawMessage `json:"args"`
	Title      string          `json:"title"`

	RunAt    time.Time `json:"run_at"`
	Repeats  string    `json:"repeats,omitempty"`
	TimeZone string    `json:"time_zone"`

	CreatedBy   string     `json:"created_by"`
	CreatedByID *uuid.UUID `json:"-"`
	CreatedAt   time.Time  `json:"created_at"`

	Status    string     `json:"status"`
	LastSeq   *int64     `json:"-"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	Result    string     `json:"result,omitempty"`
	Runs      int        `json:"runs"`
}

// jobColumns keeps the SELECT list and scanJob from drifting apart.
const jobColumns = `id, customer_id, plugin, tool, args, title,
	run_at, repeats, time_zone, created_by, created_by_id, created_at,
	status, last_seq, last_run_at, result, runs`

func scanJob(row pgx.Row) (Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.CustomerID, &j.Plugin, &j.Tool, &j.Args, &j.Title,
		&j.RunAt, &j.Repeats, &j.TimeZone, &j.CreatedBy, &j.CreatedByID, &j.CreatedAt,
		&j.Status, &j.LastSeq, &j.LastRunAt, &j.Result, &j.Runs)
	return j, err
}

// CreateJob records the intent. The caller arms the timer afterwards; if that
// fails the row is removed, because a job nothing will ever fire is worse than
// no job — it is a promise on a screen.
func (db *DB) CreateJob(ctx context.Context, j Job) (Job, error) {
	j.Plugin, j.Tool = strings.TrimSpace(j.Plugin), strings.TrimSpace(j.Tool)
	if j.Plugin == "" || j.Tool == "" {
		return Job{}, errors.New("store: a job needs a plugin and a tool")
	}
	if j.RunAt.IsZero() {
		return Job{}, errors.New("store: a job needs a time to run")
	}
	if j.ID == uuid.Nil {
		j.ID = uuid.New()
	}
	if len(j.Args) == 0 {
		j.Args = json.RawMessage(`{}`)
	}
	if strings.TrimSpace(j.TimeZone) == "" {
		j.TimeZone = "UTC"
	}

	row := db.pool.QueryRow(ctx, `
		INSERT INTO scheduled_jobs
			(id, customer_id, plugin, tool, args, title,
			 run_at, repeats, time_zone, created_by, created_by_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+jobColumns,
		j.ID, j.CustomerID, j.Plugin, j.Tool, j.Args, strings.TrimSpace(j.Title),
		j.RunAt.UTC(), strings.TrimSpace(j.Repeats), j.TimeZone,
		j.CreatedBy, j.CreatedByID)

	created, err := scanJob(row)
	if err != nil {
		return Job{}, fmt.Errorf("store: create job: %w", err)
	}
	return created, nil
}

// DeleteJob removes a row outright. Used when arming the timer failed, never
// to retire finished work — a job that ran is history, and history is kept.
func (db *DB) DeleteJob(ctx context.Context, id uuid.UUID) error {
	_, err := db.pool.Exec(ctx, `DELETE FROM scheduled_jobs WHERE id = $1`, id)
	return err
}

// GetJob reads one.
func (db *DB) GetJob(ctx context.Context, id uuid.UUID) (Job, error) {
	j, err := scanJob(db.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM scheduled_jobs WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNoJob
	}
	if err != nil {
		return Job{}, fmt.Errorf("store: get job: %w", err)
	}
	return j, nil
}

// ErrNoJob means the id names nothing. Separate from a query failure because
// a firing that finds no row is a cancelled job, not a broken database.
var ErrNoJob = errors.New("store: no such job")

/*
ListJobs answers the screen.

Pending work first and soonest first, because that is the question being asked;
finished work after it, newest first, because that is the other one. A customer
of nil lists everything, which is what an administrator looking at the whole
deployment wants.
*/
func (db *DB) ListJobs(ctx context.Context, customer *uuid.UUID, limit int) ([]Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.pool.Query(ctx, `
		SELECT `+jobColumns+`
		FROM scheduled_jobs
		WHERE ($1::uuid IS NULL OR customer_id = $1)
		ORDER BY
			CASE WHEN status IN ('scheduled', 'running') THEN 0 ELSE 1 END,
			CASE WHEN status IN ('scheduled', 'running') THEN run_at END ASC,
			COALESCE(last_run_at, created_at) DESC
		LIMIT $2`, customer, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list jobs: %w", err)
	}
	defer rows.Close()

	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// PendingJobs is every job still armed, for reconciling Postgres against the
// timers NATS is holding after a restart.
func (db *DB) PendingJobs(ctx context.Context) ([]Job, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT `+jobColumns+` FROM scheduled_jobs WHERE status = 'scheduled' ORDER BY run_at`)
	if err != nil {
		return nil, fmt.Errorf("store: pending jobs: %w", err)
	}
	defer rows.Close()

	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

/*
ClaimJob takes a job from scheduled to running, or says somebody else has it.

This is what makes at-least-once delivery safe. JetStream will redeliver a
message it never saw acknowledged, and a redelivered firing must not make a
second change to somebody's phone system. The stream sequence is the key:
monotonic, needs no clock, and unambiguous across a restart. A redelivery
carries the sequence that already ran and is refused here; a genuine second
firing of a repeating job carries a higher one and is not.
*/
func (db *DB) ClaimJob(ctx context.Context, id uuid.UUID, seq uint64) (Job, bool, error) {
	row := db.pool.QueryRow(ctx, `
		UPDATE scheduled_jobs
		SET status = 'running', last_seq = $2, last_run_at = now()
		WHERE id = $1
		  AND status = 'scheduled'
		  AND (last_seq IS NULL OR last_seq < $2)
		RETURNING `+jobColumns, id, int64(seq))

	j, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("store: claim job: %w", err)
	}
	return j, true, nil
}

/*
FinishJob records what happened.

A repeating job goes back to scheduled and waits for its next turn. A one-shot
is over either way — there is no automatic retry, and that is deliberate: a
write to a phone system that failed for an unknown reason may or may not have
landed, and repeating it unattended is how one closure becomes two. It is left
failed, with what the far end said, for a person to look at.
*/
func (db *DB) FinishJob(ctx context.Context, id uuid.UUID, ok bool, result string) error {
	_, err := db.pool.Exec(ctx, `
		UPDATE scheduled_jobs
		SET status = CASE
				WHEN NOT $2 THEN 'failed'
				WHEN repeats <> '' THEN 'scheduled'
				ELSE 'done'
			END,
			result = $3,
			runs = runs + 1
		WHERE id = $1 AND status = 'running'`, id, ok, trimResult(result))
	if err != nil {
		return fmt.Errorf("store: finish job: %w", err)
	}
	return nil
}

// CancelJob disarms one. Only pending work can be cancelled; a job that has
// already run is history and says so.
func (db *DB) CancelJob(ctx context.Context, id uuid.UUID, by string) (Job, error) {
	row := db.pool.QueryRow(ctx, `
		UPDATE scheduled_jobs
		SET status = 'cancelled', result = $2
		WHERE id = $1 AND status = 'scheduled'
		RETURNING `+jobColumns, id, "cancelled by "+strings.TrimSpace(by))

	j, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNoJob
	}
	if err != nil {
		return Job{}, fmt.Errorf("store: cancel job: %w", err)
	}
	return j, nil
}

/*
SweepStuckJobs closes out work that was interrupted.

A job left running is one whose process died between the change going out and
the answer coming back — so nobody knows whether it happened. Marking it failed
after a grace period says exactly that, in the list, where somebody will see
it. Quietly running it again would be the other choice, and it is the wrong one
for the same reason FinishJob does not retry.
*/
func (db *DB) SweepStuckJobs(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := db.pool.Exec(ctx, `
		UPDATE scheduled_jobs
		SET status = 'failed',
		    result = 'interrupted while running; whether it took effect is unknown'
		WHERE status = 'running' AND last_run_at < now() - $1::interval`,
		olderThan.String())
	if err != nil {
		return 0, fmt.Errorf("store: sweep stuck jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// MissJob marks work whose moment passed while nothing was running to fire it.
func (db *DB) MissJob(ctx context.Context, id uuid.UUID) error {
	_, err := db.pool.Exec(ctx, `
		UPDATE scheduled_jobs
		SET status = 'failed',
		    result = 'missed: its time passed while Azir was not running'
		WHERE id = $1 AND status = 'scheduled'`, id)
	return err
}

// trimResult keeps one long refusal from becoming a column of them.
func trimResult(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		return s[:497] + "..."
	}
	return s
}
