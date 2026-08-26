-- The boot backfill probes (tenant_id, data_type_slug) once per datatype x
-- binding. Every slot index is partial on "slot IS NOT NULL", the exact inverse
-- of the probe's "IS NULL", so in steady state the probe matches nothing, never
-- short-circuits on LIMIT, and sequentially scans the whole table -- before the
-- HTTP server starts, on every replica, on every deploy.
CREATE INDEX IF NOT EXISTS "order_tenant_id_data_type_slug" ON "orders" ("tenant_id", "data_type_slug");
