package scheduler_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/dreulavelle/azir/internal/scheduler"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/internal/testsupport"
)

var testDSN string

func TestMain(m *testing.M) {
	ctx := context.Background()
	dsn, _, stop, err := testsupport.PostgresDSN(ctx)
	if err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
	testDSN = dsn
	code := m.Run()
	stop()
	os.Exit(code)
}

// ran records what the scheduler asked for, standing in for the API layer that
// normally owns the gates.
type ran struct {
	mu   sync.Mutex
	jobs []store.Job
	fail error
}

func (r *ran) RunJob(_ context.Context, job store.Job) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs = append(r.jobs, job)
	return "", r.fail
}

func (r *ran) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.jobs)
}

// harness wires a real embedded NATS and a real database, because the subject
// here is JetStream's own scheduling and Postgres's own constraints.
func harness(t *testing.T) (context.Context, *scheduler.Scheduler, *store.DB, *ran) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	nc, _ := testsupport.NATS(t)
	db := testsupport.DB(t, testDSN)

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	runner := &ran{}
	sched, err := scheduler.Setup(ctx, js, db, quiet, runner)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, sched, db, runner
}

func aJob(t *testing.T, db *store.DB, when time.Time) store.Job {
	t.Helper()
	ctx := context.Background()
	customer, err := db.CreateCustomer(ctx, "Acme Dental")
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, "ann@example.com", "Ann", "admin", "a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	job, err := db.CreateJob(ctx, store.Job{
		CustomerID: &customer.ID, Plugin: "3cx", Tool: "set_office_hours",
		Title: "Back to normal hours", RunAt: when, TimeZone: "America/Chicago",
		CreatedBy: user.Email, CreatedByID: &user.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

/*
Work whose moment passed while nothing was running is not run late.

A restart that takes four hours must not end with four hours of changes landing
on customers' phone systems at once, in an order nobody chose, hours after the
moment they were meant for. Marked missed instead, in the list, where somebody
decides what to do about it.
*/
func TestWorkMissedDuringAnOutageIsNotFiredLate(t *testing.T) {
	ctx, sched, db, runner := harness(t)

	stale := aJob(t, db, time.Now().Add(-4*time.Hour))
	if err := sched.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetJob(ctx, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.JobFailed {
		t.Errorf("work from four hours ago is %q, want failed", got.Status)
	}
	if got.Result == "" {
		t.Error("nothing says it was missed")
	}

	go func() { _ = sched.Run(ctx) }()
	time.Sleep(2 * time.Second)
	if n := runner.count(); n != 0 {
		t.Errorf("missed work ran anyway, %d times", n)
	}
}

/*
Work missed by seconds is still run.

The other half of the same judgement. A deployment that restarts for twenty
seconds should not silently drop the change that fell inside it — the moment
has barely passed and running it now is what everybody meant.
*/
func TestWorkMissedByAMomentStillRuns(t *testing.T) {
	ctx, sched, db, runner := harness(t)

	recent := aJob(t, db, time.Now().Add(-20*time.Second))
	go func() { _ = sched.Run(ctx) }()
	if err := sched.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && runner.count() == 0 {
		time.Sleep(100 * time.Millisecond)
	}
	if n := runner.count(); n != 1 {
		t.Fatalf("work missed by twenty seconds ran %d times, want 1", n)
	}

	got, err := db.GetJob(ctx, recent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.JobDone {
		t.Errorf("it reads %q, want done", got.Status)
	}
}

/*
A job that lost its timer gets it back.

The row is the intent and the stream is only the alarm clock, so a stream that
was recreated — or a row written when arming then failed — must not leave a
promise on a screen that nothing will keep.
*/
func TestAJobThatLostItsTimerIsArmedAgain(t *testing.T) {
	ctx, sched, db, runner := harness(t)

	// Written but never armed, which is what an interrupted create looks like.
	job := aJob(t, db, time.Now().Add(2*time.Second))
	go func() { _ = sched.Run(ctx) }()

	time.Sleep(3 * time.Second)
	if n := runner.count(); n != 0 {
		t.Fatalf("a job nothing armed fired %d times", n)
	}

	if err := sched.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && runner.count() == 0 {
		time.Sleep(100 * time.Millisecond)
	}
	if n := runner.count(); n != 1 {
		t.Fatalf("the re-armed job ran %d times, want 1", n)
	}
	if runner.jobs[0].ID != job.ID {
		t.Errorf("something else ran: %s", runner.jobs[0].ID)
	}
}

/*
A schedule the server will not accept is refused at once.

The failure mode this exists to prevent is the quiet one: a pattern the server
cannot parse is dropped from its tracker, so the job simply never happens and
nothing anywhere says so. Refusing on the way in turns that into a message on
the screen of the person who typed it.
*/
func TestASchedulePatternTheServerRejectsFailsImmediately(t *testing.T) {
	ctx, sched, db, _ := harness(t)

	job := aJob(t, db, time.Now().Add(time.Hour))
	job.Repeats = "not a cron expression"
	if err := sched.Arm(ctx, job); err == nil {
		t.Error("a nonsense schedule was accepted; it would simply never have fired")
	}

	job.Repeats = "0 0 18 * * 5"
	job.TimeZone = "Mars/Olympus_Mons"
	if err := sched.Arm(ctx, job); err == nil {
		t.Error("a time zone that does not exist was accepted")
	}
}

// Disarming removes the alarm, not just the row. Tested here rather than only
// through the API because purging the wrong subject would leave a live timer
// behind a cancelled job, which is the one failure nobody would see coming.
func TestDisarmingRemovesTheTimer(t *testing.T) {
	ctx, sched, db, runner := harness(t)

	job := aJob(t, db, time.Now().Add(3*time.Second))
	if err := sched.Arm(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := sched.Disarm(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	go func() { _ = sched.Run(ctx) }()

	time.Sleep(6 * time.Second)
	if n := runner.count(); n != 0 {
		t.Errorf("a disarmed job fired %d times", n)
	}
}
