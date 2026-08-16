-- SQLite schema. Timestamps are ISO-8601 text, which sorts correctly
-- lexicographically and stays readable when someone opens the file directly.
-- Identifiers are UUID text rather than integers so that a customer id can be
-- handed to a plugin as an opaque handle without revealing row counts.

-- The customer spine. Azir owns this entity so that it is not a Syncro app:
-- external systems map onto a customer rather than defining one. A customer
-- with no identities is valid -- a walk-in with no PSA record still gets memory.
CREATE TABLE customers (
    id           TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX customers_display_name_key ON customers (lower(display_name));

-- One row per external system that knows this customer. The primary key means
-- a given external record maps to exactly one Azir customer, so identities
-- cannot silently fork.
CREATE TABLE customer_identities (
    customer_id TEXT NOT NULL REFERENCES customers (id) ON DELETE CASCADE,
    plugin      TEXT NOT NULL,
    external_id TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (plugin, external_id)
);

CREATE INDEX customer_identities_customer_idx ON customer_identities (customer_id);

-- Credentials never leave this table in plaintext. Envelope encryption: the
-- payload is sealed with a per-secret data key, and that data key is wrapped
-- with the versioned master key. Rotation re-wraps data keys and never has to
-- touch ciphertext.
--
-- customer_id is nullable because some credentials are deployment-wide (a
-- model provider key) rather than per-customer. The unique index coalesces it
-- so that a NULL scope still collides with itself.
CREATE TABLE credentials (
    id          TEXT PRIMARY KEY,
    customer_id TEXT REFERENCES customers (id) ON DELETE CASCADE,
    plugin      TEXT NOT NULL,
    kind        TEXT NOT NULL,
    dek_wrapped BLOB NOT NULL,
    dek_nonce   BLOB NOT NULL,
    ciphertext  BLOB NOT NULL,
    nonce       BLOB NOT NULL,
    key_version INTEGER NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX credentials_scope_key
    ON credentials (plugin, kind, COALESCE(customer_id, ''));

-- Discovery proposes; an administrator approves. A tool appearing in $SRV
-- lands here as 'pending' and stays unusable until someone activates it.
-- Without this, anyone able to start a container could extend what Azir does.
CREATE TABLE capabilities (
    plugin        TEXT NOT NULL,
    tool          TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending', 'approved', 'rejected')),
    provides      TEXT NOT NULL DEFAULT '[]',  -- JSON array
    decided_by    TEXT,
    decided_at    TEXT,
    first_seen_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_seen_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (plugin, tool)
);

-- Append-only mirror of the JetStream audit subject. Identifiers and outcomes
-- only: the audit trail must never become the place credentials leak.
CREATE TABLE audit_log (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at   TEXT NOT NULL,
    actor_user_id TEXT NOT NULL,
    action        TEXT NOT NULL,
    plugin        TEXT,
    tool          TEXT,
    customer_id   TEXT,
    outcome       TEXT NOT NULL,
    detail        TEXT
);

CREATE INDEX audit_log_occurred_idx ON audit_log (occurred_at DESC);
CREATE INDEX audit_log_customer_idx ON audit_log (customer_id, occurred_at DESC);
