-- Who chooses the model, and what the assistant is told before it starts.
--
-- Two separate decisions live here, and they are separate on purpose.
--
-- The first is a policy question an administrator owns: some deployments want
-- every technician on one approved model (a contract with a provider, a data
-- residency commitment, a per-token budget), and some are happy for a
-- technician to reach for a bigger model when a ticket is hard. Neither is
-- wrong, so it is a setting rather than a decision baked into the product.
--
-- The second is the house voice. The shipped prompt states the boundary the
-- product depends on — that the assistant cannot write anywhere, and that
-- ticket text is evidence rather than instruction — and that part is not
-- editable, because it is the containment. What an MSP can add is how they
-- want their team spoken to: house tone, escalation conventions, the phrases
-- their customers expect. That is appended, never substituted.

ALTER TABLE assistant_config
    -- 'fixed'  — everyone uses the model above.
    -- 'listed' — a technician may pick from allowed_models.
    -- 'free'   — a technician may name any model the provider accepts.
    ADD COLUMN model_choice TEXT NOT NULL DEFAULT 'fixed'
        CHECK (model_choice IN ('fixed', 'listed', 'free')),

    -- The models offered under 'listed'. Ignored by the other two, kept rather
    -- than cleared so switching policy back and forth does not lose the list
    -- somebody curated.
    ADD COLUMN allowed_models TEXT[] NOT NULL DEFAULT '{}',

    -- Appended to the shipped prompt. Bounded because it is sent on every
    -- request, and because a prompt long enough to exceed this is a document
    -- that belongs in the documentation the assistant can already search.
    ADD COLUMN house_prompt TEXT NOT NULL DEFAULT ''
        CHECK (length(house_prompt) <= 4000);

-- max_steps is no longer read. The assistant stops when it stops making
-- progress rather than after a fixed number of lookups, because the old limit
-- cut off legitimate work — reading three tickets, the customer's equipment and
-- the documentation is a normal amount of reading for a hard question, and
-- being told "I kept looking things up" after all of it helped nobody.
--
-- The column stays for now so a rollback to the previous binary still starts.
COMMENT ON COLUMN assistant_config.max_steps IS
    'Unused since 0010. The engine stops on repetition, not on a count.';

-- Which model actually answered, per conversation.
--
-- Recorded rather than inferred: the configured default changes over time, and
-- "why did this answer look different last week" is a question worth being able
-- to answer. Empty means whatever the default was at the time.
ALTER TABLE conversations
    ADD COLUMN model TEXT NOT NULL DEFAULT '';
