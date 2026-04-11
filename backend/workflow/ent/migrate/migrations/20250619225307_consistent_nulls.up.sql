-- We back-fill the zero-UUIDs here in order to fulfill the NOT NULL
-- constraint. However, this UPDATE query should not have any matches
-- in practice, as the corresponding schema always enforces a value
-- during the create-hook.
-- See https://github.com/pyck-ai/pyck/issues/175

-- modify "workflow-signals" table
DROP INDEX "workflowsignal_tenant_id_workflow_id_event_name_deleted_at";
UPDATE "workflow-signals" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "workflow-signals" ALTER COLUMN "created_by" SET NOT NULL;
CREATE UNIQUE INDEX "workflowsignal_tenant_id_workflow_id_event_name" ON "workflow-signals" ("tenant_id", "workflow_id", "event_name") WHERE (deleted_at IS NULL);

-- modify "workflows" table
DROP INDEX "workflow_tenant_id_name_deleted_at";
UPDATE "workflows" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "workflows" ALTER COLUMN "created_by" SET NOT NULL;
UPDATE "workflows" SET "active" = true WHERE "active" IS NULL;
ALTER TABLE "workflows" ALTER COLUMN "active" SET NOT NULL;
CREATE UNIQUE INDEX "workflow_tenant_id_name" ON "workflows" ("tenant_id", "name") WHERE (deleted_at IS NULL);
