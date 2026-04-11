DROP INDEX IF EXISTS picking.idx_event_outbox_polling;

CREATE INDEX IF NOT EXISTS idx_event_outbox_unpublished
    ON picking.event_outbox (created_at)
    WHERE published_at IS NULL AND dead_at IS NULL;
