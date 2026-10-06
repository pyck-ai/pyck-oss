-- reverse: modify "event_outbox" table
ALTER TABLE "event_outbox" ADD COLUMN "with_reply" boolean NOT NULL DEFAULT false;
