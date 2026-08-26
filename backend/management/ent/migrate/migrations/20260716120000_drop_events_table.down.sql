-- reverse: drop "events" table
CREATE TABLE "events" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "created_by" uuid NOT NULL, "updated_at" timestamptz NULL, "updated_by" uuid NULL, "deleted_at" timestamptz NULL, "deleted_by" uuid NULL, "topic" character varying NOT NULL, "name" character varying NOT NULL DEFAULT '', "description" character varying NOT NULL DEFAULT '', "example" jsonb NULL, PRIMARY KEY ("id"));
CREATE UNIQUE INDEX "events_topic_key" ON "events" ("topic");
