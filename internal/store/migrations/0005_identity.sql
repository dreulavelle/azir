-- Identity and roles.
--
-- Local accounts exist even though OIDC is the intended path: a self-hosted
-- product needs a way in when the identity provider is the thing that is
-- broken, and an admin who cannot log in cannot fix anything. The provider
-- columns are here from the start so an OIDC user is the same row shape rather
-- than a parallel table.
CREATE TABLE roles (
    name        TEXT PRIMARY KEY,
    description TEXT   NOT NULL DEFAULT '',
    -- Permissions are data, not code. Every check asks whether an actor holds
    -- a named permission, never what their role is called — so adding a custom
    -- role later is an INSERT and an admin screen, not a refactor of every
    -- call site.
    permissions TEXT[] NOT NULL DEFAULT '{}',
    -- Built-in roles cannot be deleted, or a deployment can lock itself out.
    builtin     BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id            UUID PRIMARY KEY,
    email         TEXT        NOT NULL,
    display_name  TEXT        NOT NULL DEFAULT '',
    role          TEXT        NOT NULL REFERENCES roles (name),
    -- Null for accounts that authenticate through an identity provider.
    password_hash TEXT,
    -- Set when the account came from an external provider.
    provider      TEXT,
    external_id   TEXT,
    disabled      BOOLEAN     NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ
);

CREATE UNIQUE INDEX users_email_key ON users (lower(email));
CREATE UNIQUE INDEX users_provider_key ON users (provider, external_id)
    WHERE provider IS NOT NULL;

-- Sessions hold only a hash of the token, so a database copy does not hand
-- someone a working login.
CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    user_agent TEXT,
    ip         TEXT
);

CREATE INDEX sessions_user_idx    ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

-- High churn: a row per login, deleted on logout and on expiry.
ALTER TABLE sessions SET (
    autovacuum_vacuum_scale_factor = 0.05,
    autovacuum_vacuum_threshold    = 100
);

-- Built-in roles.
--
-- Technician deliberately cannot configure plugins or manage users: those are
-- the two powers that would let someone quietly widen their own access.
INSERT INTO roles (name, description, permissions, builtin) VALUES
    ('admin', 'Full access, including plugin configuration and user management', ARRAY[
        'plugin.configure',
        'plugin.approve',
        'user.manage',
        'role.manage',
        'customer.manage',
        'credential.manage',
        'audit.read',
        'tool.read',
        'tool.write',
        'ticket.comment',
        'ticket.status',
        'ticket.assign'
    ], true),
    ('technician', 'Day-to-day helpdesk work', ARRAY[
        'tool.read',
        'tool.write',
        'ticket.comment',
        'ticket.status',
        'ticket.assign'
    ], true),
    ('viewer', 'Read-only: can look, cannot change anything anywhere', ARRAY[
        'tool.read'
    ], true);
