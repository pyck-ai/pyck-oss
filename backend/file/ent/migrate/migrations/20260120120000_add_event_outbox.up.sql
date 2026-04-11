-- Event Outbox Table for Transactional Outbox Pattern
-- This table stores events within the same transaction as entity mutations,
-- then OutboxHandler processes them asynchronously for reliable NATS publishing.

-- Create event_outbox table
CREATE TABLE IF NOT EXISTS file.event_outbox (
    id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz NULL,
    user_id uuid NULL,
    correlation_id character varying NOT NULL,
    topic character varying NOT NULL,
    payload jsonb NOT NULL,
    with_reply boolean NOT NULL DEFAULT false,
    retry_count integer NOT NULL DEFAULT 0,
    last_error character varying NULL,
    workflow_ids jsonb NULL,
    dead_at timestamptz NULL,
    PRIMARY KEY (id)
);

-- Index for finding unpublished entries ordered by creation time
-- Used by OutboxHandler polling with correlation ordering
CREATE INDEX IF NOT EXISTS idx_event_outbox_unpublished
    ON file.event_outbox (created_at)
    WHERE published_at IS NULL AND dead_at IS NULL;

-- Index for correlation group lookups
-- Used by OutboxHandler to fetch all entries for a correlation group
CREATE INDEX IF NOT EXISTS idx_event_outbox_correlation
    ON file.event_outbox (correlation_id, created_at)
    WHERE published_at IS NULL AND dead_at IS NULL;

-- Index for user-specific event queries
CREATE INDEX IF NOT EXISTS idx_event_outbox_user_created
    ON file.event_outbox (user_id, created_at);

-- Function to notify outbox handler of new entries
-- This enables low-latency processing (~ms after commit) via PostgreSQL LISTEN/NOTIFY
CREATE OR REPLACE FUNCTION file.notify_outbox_insert()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM pg_notify('outbox_events', NEW.id::text);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Trigger to fire NOTIFY after each insert
-- AFTER INSERT ensures the notification only fires if the transaction commits
CREATE OR REPLACE TRIGGER trg_event_outbox_notify
    AFTER INSERT ON file.event_outbox
    FOR EACH ROW
    EXECUTE FUNCTION file.notify_outbox_insert();

-- Add comments for documentation
COMMENT ON TABLE file.event_outbox IS 'Transactional outbox for reliable event publishing to NATS';
COMMENT ON COLUMN file.event_outbox.correlation_id IS 'Links events across services, used for NATS message deduplication';
COMMENT ON COLUMN file.event_outbox.with_reply IS 'When true, OutboxHandler waits for workflow IDs in NATS reply';
COMMENT ON COLUMN file.event_outbox.retry_count IS 'Number of failed publish attempts, entry skipped after max retries';
COMMENT ON COLUMN file.event_outbox.workflow_ids IS 'Workflow IDs returned from workflow service, stored for audit';
COMMENT ON COLUMN file.event_outbox.dead_at IS 'When set, marks the correlation group as dead for manual cleanup';
