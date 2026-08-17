-- Diagnostic snapshots: what a phone system looked like at one moment.
--
-- A 3CX support bundle is forty megabytes of somebody else's logs, packet
-- captures and configuration. None of it is kept. What is kept is the report
-- read out of it — a page of facts, a few series worth drawing, and the
-- findings — which is a few hundred kilobytes and contains nothing that was not
-- already a conclusion about the customer's own system.
--
-- Snapshots are per customer and per moment on purpose: the interesting
-- question is usually not "what is wrong" but "what changed since the one we
-- took before the complaint started".

CREATE TABLE snapshots (
    id           UUID PRIMARY KEY,
    customer_id  UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,

    -- What it came from, so a future bundle from something other than 3CX can
    -- sit beside these rather than pretending to be one.
    kind         TEXT NOT NULL DEFAULT '3cx-support-info',
    -- The uploaded file's name, which is how a person recognises which capture
    -- this was. Never the file itself.
    filename     TEXT NOT NULL DEFAULT '',
    -- When the phone system took the capture, as opposed to when it was
    -- uploaded here. Those can be days apart and only one of them explains a
    -- complaint.
    captured_at  TIMESTAMPTZ,

    report       JSONB NOT NULL,

    -- Denormalised out of the report so a list can be drawn without parsing
    -- every row's JSON.
    findings     INT NOT NULL DEFAULT 0,
    worst        TEXT NOT NULL DEFAULT '',

    uploaded_by  TEXT NOT NULL DEFAULT '',
    uploaded_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX snapshots_customer ON snapshots (customer_id, captured_at DESC NULLS LAST, uploaded_at DESC);
