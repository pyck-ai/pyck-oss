-- Rollback Event Outbox Table

-- Drop trigger first (depends on function)
DROP TRIGGER IF EXISTS trg_event_outbox_notify ON workflow.event_outbox;

-- Drop notify function
DROP FUNCTION IF EXISTS workflow.notify_outbox_insert();

-- Drop indexes
DROP INDEX IF EXISTS workflow.idx_event_outbox_user_created;
DROP INDEX IF EXISTS workflow.idx_event_outbox_published_created;

-- Drop table
DROP TABLE IF EXISTS workflow.event_outbox;
