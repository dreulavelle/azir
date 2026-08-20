package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/store"
)

/*
Work somebody asked for, to happen later.

The row is what a person reads and what makes a firing safe, so these test the
two things a stream cannot do for us: refusing a firing that has already
happened, and saying plainly when one was interrupted.
*/

// aJob is the boilerplate every test here needs: a customer to belong to, a
// person to have armed it, and a moment.
func aJob(t *testing.T, db *store.DB, when time.Time) store.Job {
	t.Helper()
	ctx := context.Background()

	customer, err := db.CreateCustomer(ctx, "Acme Dental "+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx,
		uuid.NewString()[:8]+"@example.com", "Ann", "admin", "a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	return store.Job{
		CustomerID:  &customer.ID,
		Plugin:      "3cx",
		Tool:        "set_office_hours",
		Args:        json.RawMessage(`{"department":"Fort Worth"}`),
		Title:       "Back to normal hours",
		RunAt:       when,
		TimeZone:    "America/Chicago",
		CreatedBy:   user.Email,
		CreatedByID: &user.ID,
	}
}

func TestAJobIsWrittenAndRead(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	when := time.Now().Add(time.Hour).Truncate(time.Second)
	created, err := db.CreateJob(ctx, aJob(t, db, when))
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != store.JobScheduled {
		t.Errorf("a new job is %q; nothing has run yet", created.Status)
	}

	got, err := db.GetJob(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.RunAt.Equal(when) {
		t.Errorf("run_at came back as %s, want %s", got.RunAt, when)
	}
	if got.Title != "Back to normal hours" {
		t.Errorf("title came back as %q", got.Title)
	}
	// The whole point of storing this rather than only the tool name: a person
	// reading the list has to be able to tell what it will do, and the plugin
	// has to be handed the same request it would have been handed at the time.
	// Compared as JSON rather than as text, because the column is jsonb and
	// normalises the spacing.
	var args map[string]string
	if err := json.Unmarshal(got.Args, &args); err != nil {
		t.Fatalf("args came back as %s: %v", got.Args, err)
	}
	if args["department"] != "Fort Worth" {
		t.Errorf("args came back as %s", got.Args)
	}
}

/*
A firing that already happened must not happen again.

JetStream delivers at least once, so the same firing arrives twice whenever an
acknowledgement is lost. Twice through here would be two changes to somebody's
phone system, and the second one is invisible — nobody asked for it and nothing
says it occurred.
*/
func TestASecondDeliveryOfTheSameFiringIsRefused(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	created, err := db.CreateJob(ctx, aJob(t, db, time.Now().Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	if _, claimed, err := db.ClaimJob(ctx, created.ID, 7); err != nil || !claimed {
		t.Fatalf("the first delivery did not take the job: claimed=%v err=%v", claimed, err)
	}
	// Redelivered while it is still running.
	if _, claimed, err := db.ClaimJob(ctx, created.ID, 7); err != nil || claimed {
		t.Errorf("a redelivery took a job that is already running: claimed=%v err=%v", claimed, err)
	}

	if err := db.FinishJob(ctx, created.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	// Redelivered after it finished. The sequence is the same one, so this is
	// the same firing rather than a second one.
	if _, claimed, err := db.ClaimJob(ctx, created.ID, 7); err != nil || claimed {
		t.Errorf("a redelivery took a job that has already run: claimed=%v err=%v", claimed, err)
	}

	done, err := db.GetJob(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != store.JobDone {
		t.Errorf("a one-shot that ran is %q, want done", done.Status)
	}
	if done.Runs != 1 {
		t.Errorf("it ran %d times", done.Runs)
	}
}

// Repeating work comes back around. A later firing carries a later sequence,
// which is what tells it apart from a redelivery of the last one.
func TestRepeatingWorkGoesBackToScheduled(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	job := aJob(t, db, time.Now().Add(time.Minute))
	job.Repeats = "0 0 18 * * 5"
	created, err := db.CreateJob(ctx, job)
	if err != nil {
		t.Fatal(err)
	}

	for i, seq := range []uint64{4, 9} {
		if _, claimed, err := db.ClaimJob(ctx, created.ID, seq); err != nil || !claimed {
			t.Fatalf("firing %d was refused: claimed=%v err=%v", i+1, claimed, err)
		}
		if err := db.FinishJob(ctx, created.ID, true, ""); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.GetJob(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.JobScheduled {
		t.Errorf("repeating work is %q after running; it has another turn coming", got.Status)
	}
	if got.Runs != 2 {
		t.Errorf("it ran %d times, want 2", got.Runs)
	}
}

/*
A failure is left failed.

Not retried. A write to a phone system that failed for an unknown reason may or
may not have landed, and repeating it unattended is how one early closing
becomes two. The words the far end used are kept, because they are the only
thing that tells a person which of those happened.
*/
func TestAFailedJobKeepsWhatTheFarEndSaid(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	created, err := db.CreateJob(ctx, aJob(t, db, time.Now().Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := db.ClaimJob(ctx, created.ID, 1); err != nil || !claimed {
		t.Fatal(err)
	}
	if err := db.FinishJob(ctx, created.ID, false, "the phone system refused: name already used"); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetJob(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.JobFailed {
		t.Errorf("status is %q, want failed", got.Status)
	}
	if got.Result != "the phone system refused: name already used" {
		t.Errorf("result is %q; a person needs the actual refusal", got.Result)
	}
	// And it stays failed: nothing re-arms it behind anyone's back.
	if _, claimed, _ := db.ClaimJob(ctx, created.ID, 2); claimed {
		t.Error("a failed job was picked up again without anyone asking")
	}
}

/*
Work interrupted mid-flight says so.

The process died between the change going out and the answer coming back, so
nobody knows whether it happened. That is exactly what the row should say —
and what it should not say is "done", or nothing at all.
*/
func TestInterruptedWorkIsClosedOutHonestly(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	created, err := db.CreateJob(ctx, aJob(t, db, time.Now().Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := db.ClaimJob(ctx, created.ID, 1); err != nil || !claimed {
		t.Fatal(err)
	}
	// Still well inside the grace: a job running for a few seconds is a job
	// running, not a job that died.
	if n, err := db.SweepStuckJobs(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("swept %d jobs that were merely busy (err=%v)", n, err)
	}

	if _, err := db.Pool().Exec(ctx,
		`UPDATE scheduled_jobs SET last_run_at = now() - interval '1 hour' WHERE id = $1`,
		created.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := db.SweepStuckJobs(ctx, 15*time.Minute); err != nil || n != 1 {
		t.Fatalf("swept %d jobs, want 1 (err=%v)", n, err)
	}

	got, err := db.GetJob(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.JobFailed {
		t.Errorf("interrupted work is %q, want failed", got.Status)
	}
	if got.Result == "" {
		t.Error("nothing says what happened to it")
	}
}

// Cancelling disarms pending work and refuses work that is already over,
// because "cancelled" would be a lie about something that already happened.
func TestOnlyPendingWorkCanBeCancelled(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	created, err := db.CreateJob(ctx, aJob(t, db, time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := db.CancelJob(ctx, created.ID, "ann@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != store.JobCancelled {
		t.Errorf("status is %q, want cancelled", cancelled.Status)
	}
	if _, claimed, _ := db.ClaimJob(ctx, created.ID, 1); claimed {
		t.Error("a cancelled job still fired")
	}
	if _, err := db.CancelJob(ctx, created.ID, "ann@example.com"); err == nil {
		t.Error("cancelling twice was accepted")
	}
}

/*
The list answers the screen's question first.

Pending work, soonest first, is what somebody opens the page to see. History
comes after it. Getting this backwards buries the one thing that is still going
to happen under everything that already did.
*/
func TestPendingWorkComesFirstAndSoonestFirst(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	base := aJob(t, db, time.Now().Add(time.Hour))
	customer := base.CustomerID

	later := base
	later.Title, later.RunAt = "later", time.Now().Add(4*time.Hour)
	sooner := base
	sooner.Title, sooner.RunAt = "sooner", time.Now().Add(time.Hour)
	past := base
	past.Title, past.RunAt = "already done", time.Now().Add(2*time.Hour)

	for _, j := range []store.Job{later, sooner, past} {
		if _, err := db.CreateJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	// Retire one of them, so history and pending are both represented.
	all, err := db.ListJobs(ctx, customer, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range all {
		if j.Title != "already done" {
			continue
		}
		if _, claimed, err := db.ClaimJob(ctx, j.ID, 1); err != nil || !claimed {
			t.Fatal(err)
		}
		if err := db.FinishJob(ctx, j.ID, true, ""); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.ListJobs(ctx, customer, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sooner", "later", "already done"}
	if len(got) != len(want) {
		t.Fatalf("got %d jobs, want %d", len(got), len(want))
	}
	for i, title := range want {
		if got[i].Title != title {
			t.Errorf("position %d is %q, want %q", i, got[i].Title, title)
		}
	}
}

// One customer's work, not the deployment's — the screen is opened from a
// customer, and other people's scheduled changes are not its business.
func TestJobsAreListedPerCustomer(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	mine, err := db.CreateJob(ctx, aJob(t, db, time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateJob(ctx, aJob(t, db, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}

	got, err := db.ListJobs(ctx, mine.CustomerID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != mine.ID {
		t.Errorf("got %d jobs for one customer, want just theirs", len(got))
	}

	everything, err := db.ListJobs(ctx, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(everything) != 2 {
		t.Errorf("the whole deployment has %d scheduled jobs, want 2", len(everything))
	}
}
