-- Somewhere for a connected system to tell us something changed.
--
-- Syncro sends webhooks and does not sign them: there is no shared secret, no
-- HMAC header, nothing to verify a delivery against. That rules out the usual
-- design, so this one does not depend on verification at all.
--
-- Two things make an unsigned public endpoint safe here:
--
--   1. The URL carries a long random secret that Azir generates. It is a
--      bearer credential in a path, which is weaker than a signature — URLs
--      end up in logs — so it is rotatable from the console and is not the
--      only thing standing between a stranger and the data.
--
--   2. Nothing in the payload is believed. A delivery is treated as "something
--      about ticket 4210 changed", never as the new state of ticket 4210. Azir
--      responds by forgetting what it cached and fetching again with its own
--      credentials. A forged delivery therefore costs one wasted API call and
--      cannot put a single invented word in front of a technician or the
--      assistant — which matters more than usual here, because the assistant
--      reads ticket text and a fake ticket would be a prompt-injection channel
--      aimed straight at it.
CREATE TABLE webhook_endpoints (
    plugin      TEXT        PRIMARY KEY,
    -- The random component of the URL. Rotating it is how you revoke a URL
    -- that leaked without touching anything else.
    secret      TEXT        NOT NULL,
    -- Purely so an operator can see whether the thing is actually wired up.
    -- A webhook that was configured wrong is otherwise silent forever.
    last_seen   TIMESTAMPTZ,
    deliveries  BIGINT      NOT NULL DEFAULT 0,
    rejected    BIGINT      NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX webhook_endpoints_secret ON webhook_endpoints (secret);
