-- reverse: add column "stopped_at" to table: "workflow-signals"
ALTER TABLE "workflow-signals" DROP COLUMN "stopped_at";
-- reverse: create index "workflowsignal_tenant_id_worker_id" to table: "workflow-signals"
DROP INDEX "workflowsignal_tenant_id_worker_id";
-- reverse: modify "workflow-signals" table
-- NOTE: the legacy rows deleted by the up migration are not restored.
ALTER TABLE "workflow-signals" ALTER COLUMN "worker_id" DROP NOT NULL, ALTER COLUMN "expires_at" DROP NOT NULL;
