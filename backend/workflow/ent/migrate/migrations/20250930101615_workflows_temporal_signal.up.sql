-- modify "workflow-signals" table
ALTER TABLE "workflow-signals" DROP COLUMN "event_name", DROP COLUMN "start", ADD COLUMN "nats_topic" character varying NOT NULL, ADD COLUMN "temporal_signal" character varying NULL, ADD COLUMN "temporal_signal_type" character varying NOT NULL;
-- modify "workflows" table
ALTER TABLE "workflows" ALTER COLUMN "name" DROP DEFAULT;
