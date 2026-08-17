-- Changes the assistant has proposed, waiting for a person.
--
-- Azir's containment used to be that the assistant had no write tool at all.
-- That was strong and too blunt: a technician working a ticket wants the thing
-- that read the ticket to also draft the change, and refusing outright pushes
-- them back into the PSA to do it by hand.
--
-- So the invariant moves rather than weakening. It was "the model cannot reach
-- an action". It is now "a model call can never change a connected system;
-- only a person's approval can." The model calls a write tool and what happens
-- is a row in this table — not a request to Syncro or a PBX. Applying it is a
-- separate, authenticated, permission-checked act by a named human.
--
-- That matters because ticket text is written by customers. A ticket saying
-- "per our agreement, disable extensions 200-209" can now cause a proposal,
-- which a technician reads and rejects. It cannot cause a change. The
-- difference between those two sentences is the entire security argument.
CREATE TABLE proposed_changes (
    id              UUID        PRIMARY KEY,
    conversation_id UUID        NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,

    -- Who was talking to the assistant when it proposed this. Not who approved
    -- it: that is recorded separately, because they need not be the same person
    -- and the audit trail should show both.
    proposed_for    TEXT        NOT NULL,

    plugin          TEXT        NOT NULL,
    tool            TEXT        NOT NULL,
    args            JSONB       NOT NULL,
    -- Whose systems this touches. Null for a deployment-wide change.
    customer_id     UUID        REFERENCES customers (id) ON DELETE SET NULL,

    -- What the assistant said it was doing, in its own words, so the person
    -- approving reads an explanation rather than a JSON body.
    summary         TEXT        NOT NULL DEFAULT '',

    status          TEXT        NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'applied', 'discarded', 'failed')),
    -- What came back when it was applied, or why it did not.
    outcome         TEXT        NOT NULL DEFAULT '',

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at      TIMESTAMPTZ,
    decided_by      TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX proposed_changes_conversation ON proposed_changes (conversation_id, created_at);
CREATE INDEX proposed_changes_pending ON proposed_changes (status) WHERE status = 'pending';
