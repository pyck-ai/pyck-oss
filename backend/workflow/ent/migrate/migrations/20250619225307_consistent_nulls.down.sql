-- reverse: create index "workflow_tenant_id_name" to table: "workflows"
DROP INDEX "workflow_tenant_id_name";
-- reverse: modify "workflows" table
ALTER TABLE "workflows" ALTER COLUMN "active" DROP NOT NULL, ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: drop index "workflow_tenant_id_name_deleted_at" from table: "workflows"
CREATE UNIQUE INDEX "workflow_tenant_id_name_deleted_at" ON "workflows" ("tenant_id", "name", "deleted_at");
-- reverse: create index "workflowsignal_tenant_id_workflow_id_event_name" to table: "workflow-signals"
DROP INDEX "workflowsignal_tenant_id_workflow_id_event_name";
-- reverse: modify "workflow-signals" table
ALTER TABLE "workflow-signals" ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: drop index "workflowsignal_tenant_id_workflow_id_event_name_deleted_at" from table: "workflow-signals"
CREATE UNIQUE INDEX "workflowsignal_tenant_id_workflow_id_event_name_deleted_at" ON "workflow-signals" ("tenant_id", "workflow_id", "event_name", "deleted_at");
