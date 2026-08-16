-- Per-plugin settings, entered by an administrator in the web console.
--
-- Non-secret values only: fields a plugin marks with x-azir-secret in its
-- config schema are routed to the credentials table instead, so a password
-- typed into the same form is still sealed rather than sitting in a JSONB
-- column that any query could surface.
--
-- customer_id is nullable so a plugin can have deployment-wide settings (a
-- Syncro subdomain) alongside per-customer ones (a 3CX instance FQDN).
CREATE TABLE plugin_config (
    plugin      TEXT        NOT NULL,
    customer_id UUID        REFERENCES customers (id) ON DELETE CASCADE,
    values      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    updated_by  TEXT,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX plugin_config_scope_key
    ON plugin_config (plugin, COALESCE(customer_id, '00000000-0000-0000-0000-000000000000'::uuid));

CREATE INDEX plugin_config_customer_idx ON plugin_config (customer_id);

-- Settings are read on nearly every tool call, so they are small and hot.
ALTER TABLE plugin_config SET (
    autovacuum_vacuum_scale_factor = 0.1,
    autovacuum_vacuum_threshold    = 25
);
