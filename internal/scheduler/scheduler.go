/*
Package scheduler runs work later.

The timer is JetStream's. A message published with Nats-Schedule sits in a
stream until its moment and is then republished to a target subject, by the
server, from disk — so it survives a restart, needs no ticker, and adds no
service to a deployment that already embeds NATS. What this package adds is the
half a stream cannot hold: which job a firing belongs to, who armed it, whether
it has already run, and what the far end said when it did. That lives in
Postgres, and the two are reconciled at startup.

The unit of work is a tool call, because that is the only verb Azir has. Every
screen and every plugin reaches the outside world the same way, so scheduling a
tool call schedules anything the product can do — including whatever a plugin
written next year registers, with nothing here changing to allow it.
*/
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/dreulavelle/azir/internal/store"
)

const (
	// StreamName holds both the armed schedules and the firings they produce.
	StreamName = "AZIR_JOBS"

	// armedPrefix is where a schedule waits. One message per job: re-arming
	// replaces it, cancelling purges it.
	armedPrefix = "azir.job.at."
	// firedPrefix is where the server republishes when the moment arrives.
	// Deliberately a different token from armedPrefix — JetStream refuses a
	// schedule whose target collides with its own subject, and rightly.
	firedPrefix = "azir.job.run."

	consumerName = "azir-jobs"

	// firedTTL clears the firings. The armed messages must never expire — a
	// job scheduled for next month has to still be there next month — so the
	// stream keeps everything and the copies expire themselves instead.
	firedTTL = 24 * time.Hour

	// stuckAfter is how long a job may sit running before it is assumed to
	// have died with the process that was running it.
	stuckAfter = 15 * time.Minute

	// graceAfterMiss is how late Azir will still run work it slept through.
	// Past this, the moment has meaningfully passed and running it now would
	// be a surprise rather than a service.
	graceAfterMiss = 15 * time.Minute
)

/*
Runner performs the job.

An interface, so this package never learns what a tool is, what a customer is,
or what any of the gates are. The API layer implements it: it owns the write
gate, the approval check and the activity log, and a second implementation of
those is exactly the thing that eventually disagrees with the first.
*/
type Runner interface {
	RunJob(ctx context.Context, job store.Job) (string, error)
}

// Scheduler arms, fires and reconciles deferred work.
type Scheduler struct {
	js     jetstream.JetStream
	stream jetstream.Stream
	db     *store.DB
	log    *slog.Logger
	runner Runner
}

// Setup creates the stream and returns a scheduler ready to arm work.
func Setup(ctx context.Context, js jetstream.JetStream, db *store.DB, log *slog.Logger, runner Runner) (*Scheduler, error) {
	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        StreamName,
		Description: "Work somebody asked for, to happen later",
		Subjects:    []string{armedPrefix + "*", firedPrefix + "*"},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		// The server's own message scheduling, and the per-message expiry the
		// firings use to clear themselves.
		AllowMsgSchedules: true,
		AllowMsgTTL:       true,
		// One message per subject: one armed schedule per job, and one
		// outstanding firing. Re-arming replaces rather than accumulates, and
		// a repeating job that fired while nothing was consuming leaves the
		// latest firing rather than a queue of stale ones.
		MaxMsgsPerSubject: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("scheduler: create stream: %w", err)
	}
	return &Scheduler{js: js, stream: stream, db: db, log: log, runner: runner}, nil
}

/*
Arm puts a job's timer in the stream.

One-shot work is scheduled at an instant and purges its own schedule once it
has fired. Repeating work carries a cron expression and the customer's zone,
because "every Friday at six" means their Friday, not the server's.
*/
func (s *Scheduler) Arm(ctx context.Context, job store.Job) error {
	body, err := json.Marshal(map[string]string{"id": job.ID.String()})
	if err != nil {
		return fmt.Errorf("scheduler: encode job: %w", err)
	}

	opts := []jetstream.PublishOpt{
		jetstream.WithScheduleTarget(firedPrefix + job.ID.String()),
		jetstream.WithScheduleTTL(firedTTL),
	}
	if repeats := strings.TrimSpace(job.Repeats); repeats != "" {
		opts = append(opts, jetstream.WithScheduleCron(repeats))
		if zone := strings.TrimSpace(job.TimeZone); zone != "" {
			opts = append(opts, jetstream.WithScheduleTimeZone(zone))
		}
	} else {
		opts = append(opts, jetstream.WithScheduleAt(job.RunAt))
	}

	if _, err := s.js.Publish(ctx, armedPrefix+job.ID.String(), body, opts...); err != nil {
		// The server validates the pattern, the zone and the target, so a
		// wrong one is refused here rather than silently never firing.
		return fmt.Errorf("scheduler: arm job: %w", err)
	}
	return nil
}

// Disarm removes a job's timer. Purging the subject takes the armed schedule
// and any firing that has not been dealt with, which together is the whole of
// what could still make it happen.
func (s *Scheduler) Disarm(ctx context.Context, id uuid.UUID) error {
	for _, subject := range []string{armedPrefix + id.String(), firedPrefix + id.String()} {
		if err := s.stream.Purge(ctx, jetstream.WithPurgeSubject(subject)); err != nil {
			return fmt.Errorf("scheduler: disarm job: %w", err)
		}
	}
	return nil
}

/*
Run consumes firings until ctx is cancelled.

Every message here means "a job's moment arrived". Whether it actually runs is
decided by the database: the claim is what turns at-least-once delivery into
at-most-one change.
*/
func (s *Scheduler) Run(ctx context.Context) error {
	cons, err := s.stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       consumerName,
		FilterSubject: firedPrefix + "*",
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		// A firing redelivered before the claim lands is a free retry; one
		// redelivered after it is refused by the claim. Three is enough for
		// the first and harmless for the second.
		MaxDeliver: 3,
		AckWait:    2 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("scheduler: create consumer: %w", err)
	}

	consuming, err := cons.Consume(func(msg jetstream.Msg) { s.fire(ctx, msg) })
	if err != nil {
		return fmt.Errorf("scheduler: consume: %w", err)
	}
	defer consuming.Stop()

	<-ctx.Done()
	return nil
}

// fire handles one firing.
func (s *Scheduler) fire(ctx context.Context, msg jetstream.Msg) {
	id, err := uuid.Parse(strings.TrimPrefix(msg.Subject(), firedPrefix))
	if err != nil {
		// Nothing names a job here, so nothing will ever make this succeed.
		s.log.Error("a scheduled firing named no job", "subject", msg.Subject())
		_ = msg.Term()
		return
	}

	meta, err := msg.Metadata()
	if err != nil {
		s.log.Error("a scheduled firing had no sequence", "job", id, "error", err)
		_ = msg.Term()
		return
	}

	job, claimed, err := s.db.ClaimJob(ctx, id, meta.Sequence.Stream)
	if err != nil {
		s.log.Error("could not claim scheduled work", "job", id, "error", err)
		_ = msg.Nak()
		return
	}
	if !claimed {
		// Cancelled, already run, or this exact firing already handled. All
		// three mean the same thing here: do not do it again.
		_ = msg.Ack()
		return
	}

	s.log.Info("running scheduled work",
		"job", job.ID, "plugin", job.Plugin, "tool", job.Tool, "title", job.Title)

	// Deliberately not the request's context: a firing is not a request, and
	// a slow phone system should not be cut off by anything but its own
	// timeout and this process shutting down.
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	result, runErr := s.runner.RunJob(runCtx, job)
	if runErr != nil {
		if err := s.db.FinishJob(ctx, job.ID, false, runErr.Error()); err != nil {
			s.log.Error("could not record a failed job", "job", job.ID, "error", err)
		}
		s.log.Warn("scheduled work failed", "job", job.ID, "error", runErr)
		_ = msg.Ack()
		return
	}
	if err := s.db.FinishJob(ctx, job.ID, true, result); err != nil {
		s.log.Error("could not record a finished job", "job", job.ID, "error", err)
	}
	_ = msg.Ack()
}

/*
Reconcile brings the stream back in line with the database after a restart.

Two things can be wrong. A job may have lost its timer — the stream was
recreated, or the row outlived the message — in which case re-arming restores
it, and re-arming is safe because it replaces rather than adds. Or its moment
may have passed while nothing was running: within a short grace it is run
almost on time, and beyond that it is marked missed. Silently firing a change
to somebody's phone system hours after it was meant to happen is the one
outcome nobody asked for.
*/
func (s *Scheduler) Reconcile(ctx context.Context) error {
	if n, err := s.db.SweepStuckJobs(ctx, stuckAfter); err != nil {
		s.log.Warn("could not close out interrupted jobs", "error", err)
	} else if n > 0 {
		s.log.Warn("scheduled work was interrupted and did not finish", "jobs", n)
	}

	pending, err := s.db.PendingJobs(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: reconcile: %w", err)
	}

	var armed, missed int
	for _, job := range pending {
		if job.Repeats == "" && time.Since(job.RunAt) > graceAfterMiss {
			if err := s.db.MissJob(ctx, job.ID); err != nil {
				s.log.Warn("could not mark a job missed", "job", job.ID, "error", err)
				continue
			}
			missed++
			continue
		}
		// Overdue but within the grace: run it shortly, rather than at a time
		// the server will refuse for being in the past.
		if job.Repeats == "" && job.RunAt.Before(time.Now()) {
			job.RunAt = time.Now().Add(5 * time.Second)
		}
		if err := s.Arm(ctx, job); err != nil {
			s.log.Error("could not re-arm scheduled work", "job", job.ID, "error", err)
			continue
		}
		armed++
	}

	if armed > 0 || missed > 0 {
		s.log.Info("scheduled work reconciled", "armed", armed, "missed", missed)
	}
	return nil
}

// Sweep closes out interrupted jobs on a timer, for the case where the process
// stayed up and only the work died.
func (s *Scheduler) Sweep(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := s.db.SweepStuckJobs(ctx, stuckAfter); err != nil {
				if !errors.Is(err, context.Canceled) {
					s.log.Warn("could not close out interrupted jobs", "error", err)
				}
			} else if n > 0 {
				s.log.Warn("scheduled work was interrupted and did not finish", "jobs", n)
			}
		}
	}
}
