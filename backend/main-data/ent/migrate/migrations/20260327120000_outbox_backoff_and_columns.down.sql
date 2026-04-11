DROP INDEX IF EXISTS "main-data".idx_event_outbox_tenant;
DROP INDEX IF EXISTS "main-data".idx_event_outbox_correlation;
DROP INDEX IF EXISTS "main-data".idx_event_outbox_polling_v2;

-- Restore old polling index.
CREATE INDEX IF NOT EXISTS idx_event_outbox_polling
    ON "main-data".event_outbox (retry_count, correlation_id, created_at)
    WHERE published_at IS NULL AND dead_at IS NULL;

-- Restore old Ent-managed index.
CREATE INDEX IF NOT EXISTS entityeventsoutbox_published_at_created_at
    ON "main-data".event_outbox (published_at, created_at);

ALTER TABLE "main-data".event_outbox DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE "main-data".event_outbox DROP COLUMN IF EXISTS entity_id;
ALTER TABLE "main-data".event_outbox DROP COLUMN IF EXISTS entity_type;
ALTER TABLE "main-data".event_outbox DROP COLUMN IF EXISTS next_retry_at;
