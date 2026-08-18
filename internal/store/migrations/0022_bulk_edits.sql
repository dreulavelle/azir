-- A sheet somebody uploaded, and the before-and-after it turned into.
--
-- Kept between the upload and the approval because those are separate visits
-- to the screen: somebody uploads a file, checks the diff against what the
-- phone system currently says, and decides. Holding it in a browser tab would
-- lose the whole thing to a refresh.
--
-- It holds a customer's data — extension numbers and the names on them — so it
-- expires the way a diagnostic capture does, and Start fresh takes it. The
-- point of the file was the change it described; once that is applied or
-- declined, keeping a copy of somebody's phone directory serves nobody.
CREATE TABLE IF NOT EXISTS bulk_edits (
    id           UUID        PRIMARY KEY,
    customer_id  UUID        NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    filename     TEXT        NOT NULL DEFAULT '',
    uploaded_by  TEXT        NOT NULL DEFAULT '',

    -- The parsed sheet, and the mapping and plan once there is one.
    sheet        JSONB       NOT NULL,
    mapping      JSONB,
    plan         JSONB,

    status       TEXT        NOT NULL DEFAULT 'draft'
                             CHECK (status IN ('draft', 'planned', 'applied', 'cancelled')),
    -- What happened per row, once applied.
    outcome      JSONB,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at   TIMESTAMPTZ,
    decided_by   TEXT        NOT NULL DEFAULT '',
    expires_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS bulk_edits_customer_idx ON bulk_edits (customer_id, created_at DESC);
CREATE INDEX IF NOT EXISTS bulk_edits_expiry_idx   ON bulk_edits (expires_at) WHERE expires_at IS NOT NULL;
