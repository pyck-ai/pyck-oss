-- Add exponential backoff column and denormalized entity columns to outbox table.
-- Entries with next_retry_at > NOW() are excluded from polling,
-- implementing 2^retry_count second backoff (capped at 1 hour) on failure.

ALTER TABLE "main-data".event_outbox
    ADD COLUMN IF NOT EXISTS next_retry_at timestamptz NULL;

ALTER TABLE "main-data".event_outbox
    ADD COLUMN IF NOT EXISTS entity_type character varying NULL;

ALTER TABLE "main-data".event_outbox
    ADD COLUMN IF NOT EXISTS entity_id uuid NULL;

ALTER TABLE "main-data".event_outbox
    ADD COLUMN IF NOT EXISTS tenant_id uuid NULL;

-- Drop stale Ent-managed polling index (superseded by idx_event_outbox_polling_v2).
DROP INDEX IF EXISTS "main-data".entityeventsoutbox_published_at_created_at;

-- Drop old polling index (superseded by new one that includes next_retry_at).
DROP INDEX IF EXISTS "main-data".idx_event_outbox_polling;

-- New polling index: includes next_retry_at for backoff filtering.
CREATE INDEX IF NOT EXISTS idx_event_outbox_polling_v2
    ON "main-data".event_outbox (next_retry_at NULLS FIRST, correlation_id, created_at)
    WHERE published_at IS NULL AND dead_at IS NULL;

-- Index for tenant-based queries.
CREATE INDEX IF NOT EXISTS idx_event_outbox_tenant
    ON "main-data".event_outbox (tenant_id, created_at)
    WHERE published_at IS NULL AND dead_at IS NULL;

-- Correlation group index: serves step 2 of the selector (WHERE correlation_id IN (...))
-- and dead letter delete (DELETE WHERE correlation_id = ?).
CREATE INDEX IF NOT EXISTS idx_event_outbox_correlation
    ON "main-data".event_outbox (correlation_id, created_at)
    WHERE published_at IS NULL AND dead_at IS NULL;

COMMENT ON COLUMN "main-data".event_outbox.next_retry_at IS
    'Earliest time for next retry. NULL = immediately eligible. Set to NOW() + LEAST(2^retry_count, 3600) seconds on failure.';
COMMENT ON COLUMN "main-data".event_outbox.entity_type IS
    'Ent schema name (e.g., Item, Location) extracted from payload for filtering.';
COMMENT ON COLUMN "main-data".event_outbox.entity_id IS
    'Entity UUID extracted from payload for filtering.';
COMMENT ON COLUMN "main-data".event_outbox.tenant_id IS
    'Tenant UUID extracted from payload for filtering.';
