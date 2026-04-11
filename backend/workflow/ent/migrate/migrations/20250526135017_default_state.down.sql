-- reverse: create index "workflowsignal_tenant_id_workflow_id_event_name_deleted_at" to table: "workflow-signals"
DROP INDEX "workflowsignal_tenant_id_workflow_id_event_name_deleted_at";
-- reverse: create "workflow-signals" table
DROP TABLE "workflow-signals";
-- reverse: create index "workflow_tenant_id_name_deleted_at" to table: "workflows"
DROP INDEX "workflow_tenant_id_name_deleted_at";
-- reverse: create "workflows" table
DROP TABLE "workflows";
