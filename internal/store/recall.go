package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

/*
Recall over work that is already finished.

The question this exists to answer is the one a technician asks out loud before
touching anything: have we seen this before, and what did we do about it. Until
now the only way to answer it was to remember, or to search the helpdesk by hand
and read whatever came back.

It is an index, not a memory. Every row is a copy of a ticket that still exists
in the helpdesk, which is what makes it safe: it can be rebuilt from scratch, it
cannot drift into being a second and disagreeing source of truth, and dropping
it loses nothing.
*/

// Remembered is one finished ticket, as recall holds it.
type Remembered struct {
	Source      string     `json:"source"`
	ExternalID  string     `json:"id"`
	CustomerID  *uuid.UUID `json:"customer_id,omitempty"`
	Customer    string     `json:"customer,omitempty"`
	Subject     string     `json:"subject"`
	Problem     string     `json:"problem,omitempty"`
	Resolution  string     `json:"resolution,omitempty"`
	Status      string     `json:"status,omitempty"`
	ProblemType string     `json:"problem_type,omitempty"`
	OpenedAt    *time.Time `json:"opened_at,omitempty"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
	// Score is how well this matched, for ordering. Not shown to anybody: a
	// number from a ranking function means nothing to a reader.
	Score float64 `json:"-"`
}

// RememberTicket adds or updates one finished ticket in the index.
func (db *DB) RememberTicket(ctx context.Context, t Remembered) error {
	if t.Source == "" || t.ExternalID == "" {
		return fmt.Errorf("store: recall needs a source and an id")
	}
	_, err := db.pool.Exec(ctx, `
		INSERT INTO ticket_memory
			(source, external_id, customer_id, customer_name, subject, problem,
			 resolution, status, problem_type, opened_at, closed_at, indexed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, now())
		ON CONFLICT (source, external_id) DO UPDATE SET
			customer_id = EXCLUDED.customer_id,
			customer_name = EXCLUDED.customer_name,
			subject = EXCLUDED.subject,
			problem = EXCLUDED.problem,
			resolution = EXCLUDED.resolution,
			status = EXCLUDED.status,
			problem_type = EXCLUDED.problem_type,
			opened_at = EXCLUDED.opened_at,
			closed_at = EXCLUDED.closed_at,
			indexed_at = now()`,
		t.Source, t.ExternalID, t.CustomerID, t.Customer, t.Subject,
		truncateText(t.Problem, 8000), truncateText(t.Resolution, 8000),
		t.Status, t.ProblemType, t.OpenedAt, t.ClosedAt)
	if err != nil {
		return fmt.Errorf("store: remember ticket: %w", err)
	}
	return nil
}

// RecallQuery is what to look for.
type RecallQuery struct {
	Text string
	// Customer narrows to one customer's history. Their own past tickets are
	// usually the better answer, because a fix that worked on their equipment
	// is more likely to work again than one that worked somewhere else.
	Customer *uuid.UUID
	// Exclude keeps the ticket being read out of its own results.
	Exclude string
	// ResolvedOnly is the default: an unfinished ticket cannot tell anybody
	// what fixed it.
	IncludeUnresolved bool
	Limit             int
}

/*
Recall finds finished tickets that look like the question.

Two rankings added together rather than one, because they fail in different
places. Full-text handles the phrasing — somebody describing a symptom in their
own words matches a ticket that described it in different words. Trigram handles
what full-text is worst at: an error code, a model number, a product name, the
half-remembered fragment somebody types into a search box. A ticket that scores
on both is almost always the one.
*/
func (db *DB) Recall(ctx context.Context, q RecallQuery) ([]Remembered, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, fmt.Errorf("store: recall needs something to look for")
	}
	limit := q.Limit
	if limit <= 0 || limit > 50 {
		limit = 8
	}

	// The query is the input's own lexemes, joined with OR.
	//
	// websearch_to_tsquery was the obvious choice and it is the wrong one here,
	// because it joins terms with AND: "printer not printing" became
	// printer & print, and the ticket reading "Cannot print to the upstairs
	// Xerox" stems only to print — so one word the reporter happened to use
	// excluded the exact ticket they wanted. Fault descriptions are guesses at
	// wording, not queries, and requiring every guess to land is requiring
	// people to already know the answer.
	//
	// Running the text through to_tsvector first means punctuation, casing and
	// stop words are handled by the same rules that built the index, so a
	// pasted error message with colons and quotes in it cannot produce a syntax
	// error. Ranking does the discriminating: matching more terms scores
	// higher, and one incidental word costs a little rank instead of every
	// result.
	rows, err := db.pool.Query(ctx, `
		WITH ask AS (
			SELECT to_tsquery('english',
			         nullif(array_to_string(
			           tsvector_to_array(to_tsvector('english', $1)), ' | '), '')) AS q
		)
		SELECT source, external_id, customer_id, customer_name, subject,
		       problem, resolution, status, problem_type, opened_at, closed_at,
		       ts_rank(searchable, ask.q) + similarity(subject, $1) AS score
		FROM ticket_memory, ask
		WHERE (
		        (ask.q IS NOT NULL AND searchable @@ ask.q)
		        OR subject % $1
		      )
		  AND ($2::uuid IS NULL OR customer_id = $2)
		  AND ($3 = '' OR external_id <> $3)
		  AND ($4 OR resolution <> '')
		ORDER BY score DESC, closed_at DESC NULLS LAST
		LIMIT $5`,
		text, q.Customer, q.Exclude, q.IncludeUnresolved, limit)
	if err != nil {
		return nil, fmt.Errorf("store: recall: %w", err)
	}
	defer rows.Close()

	out := []Remembered{}
	for rows.Next() {
		var r Remembered
		if err := rows.Scan(&r.Source, &r.ExternalID, &r.CustomerID, &r.Customer,
			&r.Subject, &r.Problem, &r.Resolution, &r.Status, &r.ProblemType,
			&r.OpenedAt, &r.ClosedAt, &r.Score); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecallSize reports how much has been indexed, so a screen can say whether an
// empty answer means "nothing matched" or "nothing has been indexed yet".
func (db *DB) RecallSize(ctx context.Context) (total, withResolution int, err error) {
	err = db.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE resolution <> '')
		FROM ticket_memory`).Scan(&total, &withResolution)
	if err != nil {
		return 0, 0, fmt.Errorf("store: recall size: %w", err)
	}
	return total, withResolution, nil
}

// RecallProgress is how far a backfill has got.
type RecallProgress struct {
	Source   string    `json:"source"`
	LastPage int       `json:"last_page"`
	Complete bool      `json:"complete"`
	Indexed  int       `json:"indexed"`
	At       time.Time `json:"updated_at"`
}

// BackfillProgress reads where indexing stopped, so it resumes rather than
// starting again — a backfill over years of tickets is thousands of vendor
// requests and must survive a restart.
func (db *DB) BackfillProgress(ctx context.Context, source string) (RecallProgress, error) {
	var p RecallProgress
	err := db.pool.QueryRow(ctx,
		`SELECT source, last_page, complete, indexed, updated_at
		 FROM recall_progress WHERE source = $1`, source).
		Scan(&p.Source, &p.LastPage, &p.Complete, &p.Indexed, &p.At)
	if err != nil {
		// Nothing recorded is a valid state: it means start at the beginning.
		return RecallProgress{Source: source}, nil
	}
	return p, nil
}

// SetBackfillProgress records where to resume.
func (db *DB) SetBackfillProgress(ctx context.Context, p RecallProgress) error {
	_, err := db.pool.Exec(ctx, `
		INSERT INTO recall_progress (source, last_page, complete, indexed, updated_at)
		VALUES ($1,$2,$3,$4, now())
		ON CONFLICT (source) DO UPDATE SET
			last_page = EXCLUDED.last_page,
			complete = EXCLUDED.complete,
			indexed = EXCLUDED.indexed,
			updated_at = now()`,
		p.Source, p.LastPage, p.Complete, p.Indexed)
	if err != nil {
		return fmt.Errorf("store: set backfill progress: %w", err)
	}
	return nil
}
