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

// Proposal is a change the assistant has suggested and nobody has agreed to.
type Proposal struct {
	ID             uuid.UUID       `json:"id"`
	ConversationID uuid.UUID       `json:"conversation_id"`
	ProposedFor    string          `json:"proposed_for"`
	Plugin         string          `json:"plugin"`
	Tool           string          `json:"tool"`
	Args           json.RawMessage `json:"args"`
	CustomerID     *uuid.UUID      `json:"customer_id,omitempty"`
	Summary        string          `json:"summary"`
	Status         string          `json:"status"`
	Outcome        string          `json:"outcome,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	DecidedAt      *time.Time      `json:"decided_at,omitempty"`
	DecidedBy      string          `json:"decided_by,omitempty"`
}

// Proposal statuses.
const (
	ProposalPending   = "pending"
	ProposalApplied   = "applied"
	ProposalDiscarded = "discarded"
	ProposalFailed    = "failed"
)

// Propose records a change the assistant wants to make.
func (db *DB) Propose(ctx context.Context, p Proposal) (Proposal, error) {
	p.ID = uuid.New()
	p.Status = ProposalPending
	err := db.pool.QueryRow(ctx, `
		INSERT INTO proposed_changes
			(id, conversation_id, proposed_for, plugin, tool, args, customer_id, summary)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at`,
		p.ID, p.ConversationID, p.ProposedFor, p.Plugin, p.Tool, p.Args, p.CustomerID, p.Summary,
	).Scan(&p.CreatedAt)
	if err != nil {
		return Proposal{}, fmt.Errorf("store: propose change: %w", err)
	}
	return p, nil
}

// Proposals lists what a conversation has suggested, oldest first.
func (db *DB) Proposals(ctx context.Context, conversationID uuid.UUID) ([]Proposal, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT id, conversation_id, proposed_for, plugin, tool, args, customer_id,
		       summary, status, outcome, created_at, decided_at, decided_by
		FROM proposed_changes WHERE conversation_id = $1 ORDER BY created_at`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("store: list proposals: %w", err)
	}
	defer rows.Close()

	out := []Proposal{}
	for rows.Next() {
		var p Proposal
		if err := rows.Scan(&p.ID, &p.ConversationID, &p.ProposedFor, &p.Plugin, &p.Tool,
			&p.Args, &p.CustomerID, &p.Summary, &p.Status, &p.Outcome,
			&p.CreatedAt, &p.DecidedAt, &p.DecidedBy); err != nil {
			return nil, fmt.Errorf("store: scan proposal: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Proposal returns one, for the person about to approve it.
func (db *DB) Proposal(ctx context.Context, id uuid.UUID) (Proposal, error) {
	var p Proposal
	err := db.pool.QueryRow(ctx, `
		SELECT id, conversation_id, proposed_for, plugin, tool, args, customer_id,
		       summary, status, outcome, created_at, decided_at, decided_by
		FROM proposed_changes WHERE id = $1`, id).Scan(
		&p.ID, &p.ConversationID, &p.ProposedFor, &p.Plugin, &p.Tool, &p.Args,
		&p.CustomerID, &p.Summary, &p.Status, &p.Outcome,
		&p.CreatedAt, &p.DecidedAt, &p.DecidedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Proposal{}, ErrNotFound
	}
	if err != nil {
		return Proposal{}, fmt.Errorf("store: read proposal: %w", err)
	}
	return p, nil
}

// ErrAlreadyDecided means somebody got there first.
//
// Two technicians looking at the same chat is normal, and both pressing Apply
// is how one change becomes two. The status is checked in the UPDATE rather
// than before it, so the database decides the race.
var ErrAlreadyDecided = errors.New("store: this change has already been decided")

// FailProposal records that an applied change did not go through.
//
// Needed because DecideProposal only moves a row out of pending, and by the
// time an apply fails the row has already left it. Without this a change that
// was refused reads as one that happened, which is the worst possible thing for
// a record whose only job is to say what was done.
func (db *DB) FailProposal(ctx context.Context, id uuid.UUID, reason, by string) error {
	_, err := db.pool.Exec(ctx, `
		UPDATE proposed_changes
		SET status = 'failed', outcome = $1, decided_by = $2, decided_at = now()
		WHERE id = $3 AND status = 'applied'`, reason, by, id)
	if err != nil {
		return fmt.Errorf("store: mark proposal failed: %w", err)
	}
	return nil
}

// DecideProposal moves a proposal out of pending, once and only once.
func (db *DB) DecideProposal(ctx context.Context, id uuid.UUID, status, outcome, by string) error {
	tag, err := db.pool.Exec(ctx, `
		UPDATE proposed_changes
		SET status = $1, outcome = $2, decided_by = $3, decided_at = now()
		WHERE id = $4 AND status = 'pending'`, status, outcome, by, id)
	if err != nil {
		return fmt.Errorf("store: decide proposal: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAlreadyDecided
	}
	return nil
}
