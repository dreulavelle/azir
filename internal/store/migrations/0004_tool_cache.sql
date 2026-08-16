-- Cached tool results.
--
-- Not a mirror of Syncro: nothing is copied here that was not asked for, and
-- the vendor stays the source of truth. This exists so a technician working one
-- customer is not waiting on a round trip for data that has not changed, and so
-- a dashboard does not spend the vendor's rate limit re-reading a phone number.
--
-- Every row records when it was fetched, and every response says how old it is.
-- Serving stale data is acceptable; serving it while implying it is live is not.
CREATE TABLE tool_cache (
    plugin      TEXT        NOT NULL,
    tool        TEXT        NOT NULL,
    -- Hash of the canonicalised arguments plus customer scope, so two callers
    -- asking the same question share an entry and different questions do not
    -- collide.
    args_key    TEXT        NOT NULL,
    customer_id UUID        REFERENCES customers (id) ON DELETE CASCADE,
    payload     JSONB       NOT NULL,
    fetched_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (plugin, tool, args_key)
);

CREATE INDEX tool_cache_fetched_idx  ON tool_cache (fetched_at);
CREATE INDEX tool_cache_customer_idx ON tool_cache (customer_id);

-- High churn by nature: every entry is rewritten each time it goes stale.
ALTER TABLE tool_cache SET (
    autovacuum_vacuum_scale_factor = 0.02,
    autovacuum_vacuum_threshold    = 500
);
