-- Rollback Event Outbox Table

-- Drop trigger first (depends on function)
DROP TRIGGER IF EXISTS trg_event_outbox_notify ON receiving.event_outbox;

-- Drop notify function
DROP FUNCTION IF EXISTS receiving.notify_outbox_insert();

-- Drop indexes
DROP INDEX IF EXISTS receiving.idx_event_outbox_user_created;
DROP INDEX IF EXISTS receiving.idx_event_outbox_published_created;

-- Drop table
DROP TABLE IF EXISTS receiving.event_outbox;