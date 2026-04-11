-- reverse: modify "workflows" table
ALTER TABLE "workflows" ALTER COLUMN "name" SET DEFAULT '';
-- reverse: modify "workflow-signals" table
ALTER TABLE "workflow-signals" DROP COLUMN "temporal_signal_type", DROP COLUMN "temporal_signal", DROP COLUMN "nats_topic", ADD COLUMN "start" boolean NOT NULL DEFAULT false, ADD COLUMN "event_name" character varying NOT NULL DEFAULT '';
