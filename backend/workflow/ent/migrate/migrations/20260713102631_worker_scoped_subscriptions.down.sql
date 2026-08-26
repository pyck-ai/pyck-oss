-- reverse: create index "workflowsignal_tenant_id_workf_4a55b35e1cd9b2ea2fc850e7b47ba3c1" to table: "workflow-signals"
DROP INDEX "workflowsignal_tenant_id_workf_4a55b35e1cd9b2ea2fc850e7b47ba3c1";
-- reverse: create index "workflowsignal_expires_at" to table: "workflow-signals"
DROP INDEX "workflowsignal_expires_at";
-- reverse: modify "workflow-signals" table
ALTER TABLE "workflow-signals" DROP COLUMN "expires_at", DROP COLUMN "worker_id";
