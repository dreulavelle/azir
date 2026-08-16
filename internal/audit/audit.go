// Package audit records what Azir did, to an append-only JetStream stream
// mirrored into Postgres.
//
// Events carry identifiers and outcomes, never payloads. The audit trail must
// not become the place credentials leak: it is long-lived, widely readable,
// and exactly the sort of thing nobody re-reviews.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/dreulavelle/azir/internal/store"
)

const (
	// StreamName is the JetStream stream backing the audit subject.
	StreamName = "AZIR_AUDIT"
	// SubjectPrefix is where audit events are published.
	SubjectPrefix = "azir.audit"
	// consumerName is the durable consumer that mirrors into Postgres.
	consumerName = "audit-mirror"
)

// Outcomes.
const (
	OutcomeOK      = "ok"
	OutcomeDenied  = "denied"
	OutcomeFailed  = "failed"
	OutcomeRefused = "refused"
)

// Event is one auditable action.
type Event struct {
	OccurredAt  time.Time  `json:"occurred_at"`
	ActorUserID string     `json:"actor_user_id"`
	Action      string     `json:"action"`
	Plugin      string     `json:"plugin,omitempty"`
	Tool        string     `json:"tool,omitempty"`
	CustomerID  *uuid.UUID `json:"customer_id,omitempty"`
	Outcome     string     `json:"outcome"`
	// Detail is a short human-readable note. It must never contain payloads,
	// credentials, hostnames or raw vendor output.
	Detail string `json:"detail,omitempty"`
}

// Recorder publishes audit events.
type Recorder struct {
	js  jetstream.JetStream
	log *slog.Logger
}

// Setup creates or updates the audit stream and returns a Recorder.
func Setup(ctx context.Context, nc *nats.Conn, log *slog.Logger) (*Recorder, jetstream.JetStream, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, nil, fmt.Errorf("audit: jetstream: %w", err)
	}

	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        StreamName,
		Description: "Append-only record of every auditable action",
		Subjects:    []string{SubjectPrefix + ".>"},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		// Audit is retained far longer than operational data. Limits exist so
		// a runaway producer cannot fill the disk, not to expire history.
		MaxAge:     365 * 24 * time.Hour,
		Discard:    jetstream.DiscardOld,
		Duplicates: 2 * time.Minute,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("audit: create stream: %w", err)
	}

	return &Recorder{js: js, log: log}, js, nil
}

// Record publishes an event. Auditing must never block the action it records,
// so a publish failure is logged and swallowed rather than returned.
func (r *Recorder) Record(ctx context.Context, e Event) {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	if e.Outcome == "" {
		e.Outcome = OutcomeOK
	}

	body, err := json.Marshal(e)
	if err != nil {
		r.log.Error("audit marshal failed", "action", e.Action, "error", err)
		return
	}
	subject := SubjectPrefix + "." + e.Action
	if _, err := r.js.Publish(ctx, subject, body); err != nil {
		r.log.Error("audit publish failed", "action", e.Action, "error", err)
	}
}

// Mirror consumes the audit stream into Postgres so audit history is
// queryable alongside everything else. It runs until ctx is cancelled.
func Mirror(ctx context.Context, js jetstream.JetStream, db *store.DB, log *slog.Logger) error {
	stream, err := js.Stream(ctx, StreamName)
	if err != nil {
		return fmt.Errorf("audit: open stream: %w", err)
	}

	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       consumerName,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		MaxDeliver:    5,
	})
	if err != nil {
		return fmt.Errorf("audit: create consumer: %w", err)
	}

	consumeCtx, err := cons.Consume(func(msg jetstream.Msg) {
		var e Event
		if err := json.Unmarshal(msg.Data(), &e); err != nil {
			log.Error("audit decode failed", "error", err)
			// Undecodable messages will never succeed; drop rather than loop.
			_ = msg.Term()
			return
		}
		if err := insert(ctx, db, e); err != nil {
			log.Error("audit mirror failed", "action", e.Action, "error", err)
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return fmt.Errorf("audit: consume: %w", err)
	}
	defer consumeCtx.Stop()

	<-ctx.Done()
	return nil
}

func insert(ctx context.Context, db *store.DB, e Event) error {
	var customerID any
	if e.CustomerID != nil {
		customerID = e.CustomerID.String()
	}
	_, err := db.Writer().ExecContext(ctx, `
		INSERT INTO audit_log
			(occurred_at, actor_user_id, action, plugin, tool, customer_id, outcome, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.OccurredAt.UTC().Format(time.RFC3339Nano), e.ActorUserID, e.Action,
		nullable(e.Plugin), nullable(e.Tool), customerID, e.Outcome, nullable(e.Detail))
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Recent returns the newest audit entries.
func Recent(ctx context.Context, db *store.DB, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Reader().QueryContext(ctx, `
		SELECT occurred_at, actor_user_id, action, plugin, tool, customer_id, outcome, detail
		FROM audit_log ORDER BY occurred_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("audit: recent: %w", err)
	}
	defer rows.Close()

	out := []Event{}
	for rows.Next() {
		var (
			e                                Event
			occurredAt                       string
			plugin, tool, detail, customerID *string
		)
		if err := rows.Scan(&occurredAt, &e.ActorUserID, &e.Action,
			&plugin, &tool, &customerID, &e.Outcome, &detail); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, occurredAt); err == nil {
			e.OccurredAt = t.UTC()
		}
		if customerID != nil {
			if parsed, err := uuid.Parse(*customerID); err == nil {
				e.CustomerID = &parsed
			}
		}
		e.Plugin, e.Tool, e.Detail = deref(plugin), deref(tool), deref(detail)
		out = append(out, e)
	}
	return out, rows.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ErrNotConfigured signals that auditing is unavailable.
var ErrNotConfigured = errors.New("audit: not configured")
