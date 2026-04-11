-- reverse: create index "workflow_tenant_id_name_task_queue" to table: "workflows"
DROP INDEX "workflow_tenant_id_name_task_queue";
-- reverse: modify "workflows" table
ALTER TABLE "workflows" DROP COLUMN "task_queue", ADD COLUMN "filter_rule" character varying NULL, ADD COLUMN "active" boolean NOT NULL DEFAULT true;
-- reverse: modify "workflow-signals" table
ALTER TABLE "workflow-signals" DROP COLUMN "filter_rule", ADD COLUMN "data" jsonb NULL, ADD COLUMN "data_type_slug" character varying NULL, ADD COLUMN "data_type_id" uuid NULL;
