-- Per-table autovacuum. The global settings are a floor chosen for the whole
-- database, but these tables have opposite shapes: audit_log is append-only and
-- churns constantly, while customers and credentials are tiny and near-static.
-- A single compromise setting is wrong for both.

-- audit_log receives a row for every tool call and every administrative action,
-- and is only ever read by recent-first queries. Vacuum aggressively so dead
-- tuples never accumulate into a stall, and analyze rarely: the planner needs
-- almost nothing about a table that is always scanned by a fresh index range.
ALTER TABLE audit_log SET (
    autovacuum_vacuum_scale_factor  = 0.01,
    autovacuum_vacuum_threshold     = 1000,
    autovacuum_analyze_scale_factor = 0.05,
    -- Append-only tables accumulate un-frozen pages until an anti-wraparound
    -- vacuum has to read the entire table at once. Freezing early spreads that
    -- cost across ordinary vacuums instead of paying it in one stall.
    autovacuum_freeze_min_age       = 1000000,
    autovacuum_freeze_max_age       = 100000000
);

-- The spine is small and rarely written. The global scale factor would have it
-- vacuumed on a handful of row changes for no benefit; a threshold suits a
-- table that may never reach a meaningful percentage.
ALTER TABLE customers SET (
    autovacuum_vacuum_scale_factor  = 0.2,
    autovacuum_vacuum_threshold     = 50,
    autovacuum_analyze_scale_factor = 0.1
);

ALTER TABLE customer_identities SET (
    autovacuum_vacuum_scale_factor = 0.2,
    autovacuum_vacuum_threshold    = 50
);

-- Credentials are updated in place on rotation, so every rotation dirties every
-- row. That is rare but total, which is exactly the case a low threshold
-- handles well and a scale factor handles badly.
ALTER TABLE credentials SET (
    autovacuum_vacuum_scale_factor = 0.1,
    autovacuum_vacuum_threshold    = 25
);

-- Capabilities are rewritten on every discovery sweep — last_seen_at updates
-- every ten seconds per tool — so this small table produces dead tuples out of
-- all proportion to its size.
ALTER TABLE capabilities SET (
    autovacuum_vacuum_scale_factor = 0.05,
    autovacuum_vacuum_threshold    = 25
);
