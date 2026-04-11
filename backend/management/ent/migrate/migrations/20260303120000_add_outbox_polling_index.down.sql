DROP INDEX IF EXISTS management.idx_event_outbox_polling;

CREATE INDEX IF NOT EXISTS idx_event_outbox_unpublished
    ON management.event_outbox (created_at)
    WHERE published_at IS NULL AND dead_at IS NULL;
