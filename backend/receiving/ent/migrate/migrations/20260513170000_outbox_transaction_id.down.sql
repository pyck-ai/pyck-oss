-- Revert: re-introduce correlation_id, drop transaction_id and observability columns.
-- Yolo: existing rows lose their dedup key; tenant_id stays non-null.

DROP INDEX IF EXISTS receiving.idx_event_outbox_polling_v2;
DROP INDEX IF EXISTS receiving.idx_event_outbox_transaction;

ALTER TABLE receiving.event_outbox
    DROP COLUMN IF EXISTS transaction_id,
    DROP COLUMN IF EXISTS trace_id,
    DROP COLUMN IF EXISTS request_id;

ALTER TABLE receiving.event_outbox
    ADD COLUMN correlation_id character varying NOT NULL DEFAULT '';

ALTER TABLE receiving.event_outbox
    ALTER COLUMN correlation_id DROP DEFAULT;

CREATE INDEX IF NOT EXISTS idx_event_outbox_polling_v2
    ON receiving.event_outbox (next_retry_at NULLS FIRST, correlation_id, created_at)
    WHERE published_at IS NULL AND dead_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_event_outbox_correlation
    ON receiving.event_outbox (correlation_id, created_at)
    WHERE published_at IS NULL AND dead_at IS NULL;
