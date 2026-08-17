-- Sign-in through an external identity provider, alongside local accounts.
--
-- The provider says who someone is. Azir still says what they may do: the role
-- lives on the users row, not in a token claim, so a change to a group in the
-- directory cannot quietly turn a technician into an administrator here.
--
-- The client secret is not in this table. It goes to the credentials table like
-- every other secret, sealed with the same envelope encryption, so there is one
-- answer to "where do secrets live" rather than two.
CREATE TABLE auth_config (
    -- One row, enforced rather than assumed: a second row would be a second
    -- opinion about who may sign in.
    id              BOOLEAN     PRIMARY KEY DEFAULT true CHECK (id),
    enabled         BOOLEAN     NOT NULL DEFAULT false,

    -- The OIDC issuer, discovered at /.well-known/openid-configuration.
    -- Generic on purpose: Entra is the provider being targeted, but nothing
    -- here is specific to it beyond the tenant column below.
    issuer          TEXT        NOT NULL DEFAULT '',
    client_id       TEXT        NOT NULL DEFAULT '',

    -- Entra's tenant GUID. Checked against the token's tid claim on every
    -- sign-in. Without this check a single-tenant application configured
    -- against a shared endpoint will happily accept any Microsoft account in
    -- the world, which is the best known way to get this wrong.
    tenant_id       TEXT        NOT NULL DEFAULT '',

    -- Who is allowed in. An empty domain list means "anyone the provider
    -- authenticates", which is only safe when the provider is a single tenant
    -- you control.
    allowed_domains TEXT[]      NOT NULL DEFAULT '{}',

    -- Whether a successful sign-in may create an account that does not exist
    -- yet. Off by default: authenticating against a directory is not the same
    -- as being invited into this deployment.
    auto_provision  BOOLEAN     NOT NULL DEFAULT false,
    default_role    TEXT        NOT NULL DEFAULT 'viewer' REFERENCES roles (name),

    -- A deployment behind a proxy cannot infer its own public URL from the
    -- request, and a redirect URI that does not match exactly is rejected by
    -- the provider. Empty means "work it out from the request".
    redirect_url    TEXT        NOT NULL DEFAULT '',

    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by      TEXT        NOT NULL DEFAULT ''
);

INSERT INTO auth_config (id) VALUES (true);

-- One row per sign-in attempt, holding the values that must survive the round
-- trip to the provider and come back unchanged.
--
-- Server-side rather than in a cookie so that a replayed callback finds nothing
-- to match: consuming a row deletes it, which makes an authorization code
-- usable exactly once.
CREATE TABLE oidc_states (
    state      TEXT        PRIMARY KEY,
    nonce      TEXT        NOT NULL,
    -- PKCE. Protects the code even if it leaks from a redirect, a proxy log or
    -- browser history on the way back.
    verifier   TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX oidc_states_expires_idx ON oidc_states (expires_at);

-- As high-churn as sessions: a row per attempt, deleted on completion.
ALTER TABLE oidc_states SET (
    autovacuum_vacuum_scale_factor = 0.05,
    autovacuum_vacuum_threshold    = 100
);
