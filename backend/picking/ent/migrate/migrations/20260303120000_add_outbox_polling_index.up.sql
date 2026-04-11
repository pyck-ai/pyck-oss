DROP INDEX IF EXISTS picking.idx_event_outbox_unpublished;

CREATE INDEX IF NOT EXISTS idx_event_outbox_polling
    ON picking.event_outbox (retry_count, correlation_id, created_at)
    WHERE published_at IS NULL AND dead_at IS NULL;
