-- pgvector is why this is Postgres. Semantic recall over tickets,
-- conversations and memory needs an ANN index; brute-force search does not
-- survive the corpus a few years of call records and ticket history produce.
CREATE EXTENSION IF NOT EXISTS vector;

-- Trigram matching backs fuzzy customer-name lookup, so the agent can resolve
-- "Acme Dental" to a customer without an exact string.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- The customer spine. Azir owns this entity so that it is not a Syncro app:
-- external systems map onto a customer rather than defining one. A customer
-- with no identities is valid — a walk-in with no PSA record still gets memory.
CREATE TABLE customers (
    id           UUID PRIMARY KEY,
    display_name TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX customers_display_name_key ON customers (lower(display_name));
CREATE INDEX customers_display_name_trgm ON customers USING gin (display_name gin_trgm_ops);

-- One row per external system that knows this customer. The primary key means
-- a given external record maps to exactly one Azir customer, so identities
-- cannot silently fork.
CREATE TABLE customer_identities (
    customer_id UUID        NOT NULL REFERENCES customers (id) ON DELETE CASCADE,
    plugin      TEXT        NOT NULL,
    external_id TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (plugin, external_id)
);

CREATE INDEX customer_identities_customer_idx ON customer_identities (customer_id);

-- Credentials never leave this table in plaintext. Envelope encryption: the
-- payload is sealed with a per-secret data key, and that data key is wrapped
-- with the versioned master key. Rotation re-wraps data keys and never has to
-- touch ciphertext.
--
-- customer_id is nullable because some credentials are deployment-wide (a
-- model provider key) rather than per-customer.
CREATE TABLE credentials (
    id          UUID PRIMARY KEY,
    customer_id UUID        REFERENCES customers (id) ON DELETE CASCADE,
    plugin      TEXT        NOT NULL,
    kind        TEXT        NOT NULL,
    dek_wrapped BYTEA       NOT NULL,
    dek_nonce   BYTEA       NOT NULL,
    ciphertext  BYTEA       NOT NULL,
    nonce       BYTEA       NOT NULL,
    key_version INTEGER     NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A NULL customer scope must collide with itself, so it is coalesced rather
-- than left to NULL's non-equality.
CREATE UNIQUE INDEX credentials_scope_key
    ON credentials (plugin, kind, COALESCE(customer_id, '00000000-0000-0000-0000-000000000000'::uuid));

-- Discovery proposes; an administrator approves. A tool appearing in $SRV
-- lands here as 'pending' and stays unusable until someone activates it.
-- Without this, anyone able to start a container could extend what Azir does.
CREATE TABLE capabilities (
    plugin        TEXT        NOT NULL,
    tool          TEXT        NOT NULL,
    status        TEXT        NOT NULL DEFAULT 'pending'
                              CHECK (status IN ('pending', 'approved', 'rejected')),
    provides      TEXT[]      NOT NULL DEFAULT '{}',
    decided_by    TEXT,
    decided_at    TIMESTAMPTZ,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (plugin, tool)
);

-- Append-only mirror of the JetStream audit subject. Identifiers and outcomes
-- only: the audit trail must never become the place credentials leak.
CREATE TABLE audit_log (
    id            BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_user_id TEXT        NOT NULL,
    action        TEXT        NOT NULL,
    plugin        TEXT,
    tool          TEXT,
    customer_id   UUID,
    outcome       TEXT        NOT NULL,
    detail        TEXT
);

CREATE INDEX audit_log_occurred_idx ON audit_log (occurred_at DESC);
CREATE INDEX audit_log_customer_idx ON audit_log (customer_id, occurred_at DESC);
