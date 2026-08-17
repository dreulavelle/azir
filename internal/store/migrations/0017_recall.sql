-- What Azir remembers about work that is already finished.
--
-- Not a memory of conversations. The conversations are commentary; the ticket
-- is the record, and it already exists in the helpdesk. This table is an index
-- over resolved work so the question a technician actually asks — "have we seen
-- this before?" — can be answered in one query instead of by remembering.
--
-- Everything here is a copy of something authoritative, which is the property
-- that makes it safe to rebuild: if this table were dropped it could be filled
-- again from the helpdesk, and nothing would be lost. Nothing is stored here
-- that does not exist there.

CREATE TABLE ticket_memory (
    -- The connected system and its own identifier, so two helpdesks could be
    -- indexed side by side without colliding.
    source        TEXT   NOT NULL,
    external_id   TEXT   NOT NULL,

    customer_id   UUID   REFERENCES customers(id) ON DELETE SET NULL,
    customer_name TEXT   NOT NULL DEFAULT '',

    subject       TEXT   NOT NULL DEFAULT '',
    problem       TEXT   NOT NULL DEFAULT '',
    -- How it was actually put right. The most valuable column here and the
    -- one most often missing: plenty of tickets are closed without anybody
    -- writing down what fixed them.
    resolution    TEXT   NOT NULL DEFAULT '',

    status        TEXT   NOT NULL DEFAULT '',
    problem_type  TEXT   NOT NULL DEFAULT '',
    opened_at     TIMESTAMPTZ,
    closed_at     TIMESTAMPTZ,

    -- Lexical search, generated rather than maintained, so it can never drift
    -- from the text it describes. Weighted: a subject line is a better signal
    -- of what a ticket was about than a paragraph of a customer's narration,
    -- and a resolution is what the reader is actually looking for.
    searchable    tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(subject, '')),    'A') ||
        setweight(to_tsvector('english', coalesce(resolution, '')), 'B') ||
        setweight(to_tsvector('english', coalesce(problem, '')),    'C')
    ) STORED,

    -- Left empty for now, and deliberately nullable.
    --
    -- Lexical search needs no model and sends nothing anywhere, which matters
    -- here more than it would elsewhere: an embedding of a ticket is that
    -- ticket's text leaving the building. When an operator configures an
    -- embedding endpoint this column fills in and search becomes hybrid; until
    -- then it stays null and nothing is worse for it.
    embedding     vector(1536),

    indexed_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (source, external_id)
);

CREATE INDEX ticket_memory_search ON ticket_memory USING gin (searchable);

-- Trigram over the subject, because half of these searches are for a product
-- name, an error code or a model number, and those are the cases where a
-- stemmer helps least and a fuzzy substring match helps most.
CREATE INDEX ticket_memory_subject_trgm ON ticket_memory USING gin (subject gin_trgm_ops);

-- "What has this customer had trouble with before" is the other question, and
-- it is asked from a ticket that already knows whose it is.
CREATE INDEX ticket_memory_customer ON ticket_memory (customer_id, closed_at DESC);

-- Where the backfill has got to, so it can be resumed rather than restarted.
CREATE TABLE recall_progress (
    source       TEXT PRIMARY KEY,
    last_page    INT  NOT NULL DEFAULT 0,
    complete     BOOLEAN NOT NULL DEFAULT FALSE,
    indexed      INT  NOT NULL DEFAULT 0,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
