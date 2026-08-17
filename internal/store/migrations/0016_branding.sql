-- Whose product this is.
--
-- Azir is a name I chose, and an MSP reselling this to their own customers
-- should be able to choose a different one. Everything an operator would want
-- to change lives here rather than in the code: the name on the tab, the mark
-- in the corner, the line under the sign-in box, and the one colour the
-- interface treats as the product's own voice.
--
-- Deliberately one row. A deployment is one business.
CREATE TABLE branding (
    id          BOOLEAN     PRIMARY KEY DEFAULT true CHECK (id),

    -- Empty means "use what this version ships", the same rule the system
    -- prompt follows: an upgrade that improves the default still reaches a
    -- deployment that never overrode it.
    name        TEXT        NOT NULL DEFAULT '' CHECK (length(name) <= 40),
    tagline     TEXT        NOT NULL DEFAULT '' CHECK (length(tagline) <= 120),
    -- One or two characters for the square mark, when there is no logo.
    mark        TEXT        NOT NULL DEFAULT '' CHECK (length(mark) <= 2),

    -- The colour the interface uses for its own voice — the assistant, the
    -- mark, focus rings. Not the signal colours: red still means something is
    -- wrong whatever a reseller would prefer, because that meaning is the
    -- product working rather than decoration.
    accent      TEXT        NOT NULL DEFAULT '' CHECK (accent = '' OR accent ~ '^#[0-9a-fA-F]{6}$'),

    -- Stored rather than linked. A logo on somebody else's server is a request
    -- leaving the network on every page load and a broken image the day it
    -- moves, and this product's whole claim is that it does not phone out.
    logo        BYTEA,
    logo_type   TEXT        NOT NULL DEFAULT '',

    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by  TEXT        NOT NULL DEFAULT ''
);

INSERT INTO branding (id) VALUES (true);
