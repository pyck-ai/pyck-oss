-- Rollback Event Outbox Table

-- Drop trigger first (depends on function)
DROP TRIGGER IF EXISTS trg_event_outbox_notify ON inventory.event_outbox;

-- Drop notify function
DROP FUNCTION IF EXISTS inventory.notify_outbox_insert();

-- Drop indexes
DROP INDEX IF EXISTS inventory.idx_event_outbox_user_created;
DROP INDEX IF EXISTS inventory.idx_event_outbox_published_created;

-- Drop table
DROP TABLE IF EXISTS inventory.event_outbox;
