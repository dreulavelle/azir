-- The assistant: which model answers, and what was said.
--
-- The API key is not in this table. Like every other secret it goes to the
-- credentials table, sealed with the same envelope encryption, so there is one
-- answer to "where do secrets live" rather than two.
CREATE TABLE assistant_config (
    -- One row, enforced rather than assumed.
    id          BOOLEAN     PRIMARY KEY DEFAULT true CHECK (id),
    enabled     BOOLEAN     NOT NULL DEFAULT false,

    -- Which service answers, and as which model. Bring-your-own-key: Azir
    -- never proxies through a service of ours, so a customer's ticket text
    -- goes to the provider they chose and nowhere else.
    provider    TEXT        NOT NULL DEFAULT 'anthropic',
    model       TEXT        NOT NULL DEFAULT '',
    -- For a self-hosted or compatible endpoint. Empty means the provider's own.
    base_url    TEXT        NOT NULL DEFAULT '',

    -- How much history to carry into each request. Bounded because a long
    -- conversation is both expensive and worse: the model loses the thread.
    max_turns   INT         NOT NULL DEFAULT 12 CHECK (max_turns BETWEEN 1 AND 60),
    -- How many times the assistant may look something up before it must answer.
    -- A loop that never terminates is the failure mode here.
    max_steps   INT         NOT NULL DEFAULT 8 CHECK (max_steps BETWEEN 1 AND 20),

    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by  TEXT        NOT NULL DEFAULT ''
);

INSERT INTO assistant_config (id) VALUES (true);

-- A conversation belongs to the person who started it.
--
-- Deliberately not shared: a technician's working notes about a customer are
-- not the same thing as the customer record, and making every chat visible to
-- everyone would change what people are willing to ask.
CREATE TABLE conversations (
    id         UUID        PRIMARY KEY,
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title      TEXT        NOT NULL DEFAULT '',

    -- What the conversation is about, when it was opened from somewhere.
    -- A chat started from a ticket already knows which ticket, so nobody has
    -- to paste it in — which is the entire point of the product.
    subject_kind TEXT      NOT NULL DEFAULT '',
    subject_id   TEXT      NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX conversations_user_idx ON conversations (user_id, updated_at DESC);
CREATE INDEX conversations_subject_idx ON conversations (subject_kind, subject_id)
    WHERE subject_kind <> '';

CREATE TABLE messages (
    id              UUID        PRIMARY KEY,
    conversation_id UUID        NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    -- 'user', 'assistant', or 'tool' for a lookup the assistant made.
    role            TEXT        NOT NULL,
    content         TEXT        NOT NULL DEFAULT '',

    -- What the assistant looked up, for the answer's own audit trail. Names and
    -- arguments only: results are not stored, because a cached copy of customer
    -- data in a chat log is a second place it can leak from.
    steps           JSONB       NOT NULL DEFAULT '[]'::jsonb,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX messages_conversation_idx ON messages (conversation_id, created_at);

-- Chats churn: a row per message, and whole conversations get deleted.
ALTER TABLE messages SET (
    autovacuum_vacuum_scale_factor = 0.05,
    autovacuum_vacuum_threshold    = 200
);
