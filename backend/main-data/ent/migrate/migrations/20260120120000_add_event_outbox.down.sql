-- Rollback Event Outbox Table

-- Drop trigger first (depends on function)
DROP TRIGGER IF EXISTS trg_event_outbox_notify ON "main-data".event_outbox;

-- Drop notify function
DROP FUNCTION IF EXISTS "main-data".notify_outbox_insert();

-- Drop indexes
DROP INDEX IF EXISTS "main-data".idx_event_outbox_user_created;
DROP INDEX IF EXISTS "main-data".idx_event_outbox_published_created;

-- Drop table
DROP TABLE IF EXISTS "main-data".event_outbox;
