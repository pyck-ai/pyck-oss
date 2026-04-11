-- Re-add workflow_ids column to event_outbox table

ALTER TABLE receiving.event_outbox ADD COLUMN workflow_ids jsonb NULL;

COMMENT ON COLUMN receiving.event_outbox.workflow_ids IS 'Workflow IDs returned from workflow service, stored for audit';
