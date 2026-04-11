-- Drop unused workflow_ids column from event_outbox table
-- This column was only for audit purposes and is not needed

ALTER TABLE receiving.event_outbox DROP COLUMN IF EXISTS workflow_ids;
