-- Work somebody asked for, to happen later.
--
-- Deliberately not a holidays table, or a phone table. The one verb Azir has
-- is "invoke a tool" — every screen, every plugin, every change goes through
-- it — so a job is a tool call with a time on it. The scheduler never learns
-- what a holiday is, and a plugin written next year is schedulable the day it
-- registers, without a line changing here.
--
-- NATS holds the timer. This holds the intent: who wanted it, what it does,
-- whether it has happened yet, and what the phone system said when it did.
-- A message in a stream can answer none of those, and those are the questions
-- somebody asks at 4pm on a Friday.
CREATE TABLE IF NOT EXISTS scheduled_jobs (
    id           UUID        PRIMARY KEY,

    -- Nullable, because not all work belongs to a customer. Deleting a
    -- customer takes their scheduled work with them, which is the only
    -- reading of that deletion that leaves nothing armed behind it.
    customer_id  UUID        REFERENCES customers(id) ON DELETE CASCADE,

    plugin       TEXT        NOT NULL,
    tool         TEXT        NOT NULL,
    args         JSONB       NOT NULL DEFAULT '{}'::jsonb,

    -- What a person reads in the list. Written by whoever created the job,
    -- because only they know what it was for; "3cx.set_hours" is not an answer
    -- to "what is this and can I cancel it".
    title        TEXT        NOT NULL DEFAULT '',

    run_at       TIMESTAMPTZ NOT NULL,
    -- A cron expression for work that repeats, empty for work that happens
    -- once. Six fields, matching what the server parses.
    repeats      TEXT        NOT NULL DEFAULT '',
    -- The customer's zone, not the server's. "Every Friday at 6pm" means
    -- their Friday.
    time_zone    TEXT        NOT NULL DEFAULT 'UTC',

    /*
        The approval, inherited.

        Creating the schedule is the approval — a person with the permission
        said do this, and named the moment. So nothing re-asks a human when it
        fires; there is no human there. What is re-checked at that moment is
        every standing gate: the account still exists and is enabled, the role
        still carries the permission, writes are still enabled for the plugin,
        and the tool is still approved. Revoking any of those stops scheduled
        work, which is what makes them kill switches rather than paperwork.
    */
    created_by    TEXT        NOT NULL DEFAULT '',
    created_by_id UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    status       TEXT        NOT NULL DEFAULT 'scheduled'
                             CHECK (status IN ('scheduled', 'running', 'done', 'failed', 'cancelled')),

    -- The stream sequence of the message that last set this job running.
    -- JetStream delivers at least once, so a redelivery has to be told from a
    -- second firing. A sequence is monotonic and needs no clock to compare.
    last_seq     BIGINT,
    last_run_at  TIMESTAMPTZ,
    -- What happened, in the words the phone system used.
    result       TEXT        NOT NULL DEFAULT '',
    runs         INTEGER     NOT NULL DEFAULT 0
);

-- The list a technician opens: one customer's pending work, soonest first.
CREATE INDEX IF NOT EXISTS scheduled_jobs_customer_idx
    ON scheduled_jobs (customer_id, run_at);

-- Startup reconciliation and the stuck-job sweep both ask for jobs by state.
CREATE INDEX IF NOT EXISTS scheduled_jobs_pending_idx
    ON scheduled_jobs (status, run_at) WHERE status IN ('scheduled', 'running');
