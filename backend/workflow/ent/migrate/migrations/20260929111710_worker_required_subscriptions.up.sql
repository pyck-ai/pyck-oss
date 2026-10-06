-- Every subscription must be owned by a worker and carry an expiry. Legacy
-- shared rows (NULL worker_id, never expiring) can no longer be produced or
-- expired, so drop them; workers re-register their subscriptions on start.
DELETE FROM "workflow-signals" WHERE "worker_id" IS NULL;
-- Defensive: worker-owned rows always had an expiry, but never leave a NULL behind.
DELETE FROM "workflow-signals" WHERE "expires_at" IS NULL;
-- modify "workflow-signals" table
ALTER TABLE "workflow-signals" ALTER COLUMN "worker_id" SET NOT NULL, ALTER COLUMN "expires_at" SET NOT NULL;
-- create index "workflowsignal_tenant_id_worker_id" to table: "workflow-signals"
CREATE INDEX "workflowsignal_tenant_id_worker_id" ON "workflow-signals" ("tenant_id", "worker_id");
-- add the clean-stop hint set by unregisterWorker and cleared by re-registration
ALTER TABLE "workflow-signals" ADD COLUMN "stopped_at" timestamptz NULL;
