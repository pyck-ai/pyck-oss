-- modify "workflow-signals" table
ALTER TABLE "workflow-signals" ADD COLUMN "worker_id" character varying NULL, ADD COLUMN "expires_at" timestamptz NULL;
-- create index "workflowsignal_expires_at" to table: "workflow-signals"
CREATE INDEX "workflowsignal_expires_at" ON "workflow-signals" ("expires_at");
-- create index "workflowsignal_tenant_id_workf_4a55b35e1cd9b2ea2fc850e7b47ba3c1" to table: "workflow-signals"
CREATE UNIQUE INDEX "workflowsignal_tenant_id_workf_4a55b35e1cd9b2ea2fc850e7b47ba3c1" ON "workflow-signals" ("tenant_id", "workflow_id", "worker_id", "nats_topic", "temporal_signal_type", "temporal_signal") WHERE (deleted_at IS NULL);
