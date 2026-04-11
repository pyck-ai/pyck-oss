-- modify "workflow-signals" table
ALTER TABLE "workflow-signals" DROP COLUMN "data_type_id", DROP COLUMN "data_type_slug", DROP COLUMN "data", ADD COLUMN "filter_rule" character varying NULL;
-- modify "workflows" table
ALTER TABLE "workflows" DROP COLUMN "active", DROP COLUMN "filter_rule", ADD COLUMN "task_queue" character varying NOT NULL DEFAULT 'default';
-- create index "workflow_tenant_id_name_task_queue" to table: "workflows"
CREATE UNIQUE INDEX "workflow_tenant_id_name_task_queue" ON "workflows" ("tenant_id", "name", "task_queue") WHERE (deleted_at IS NULL);
