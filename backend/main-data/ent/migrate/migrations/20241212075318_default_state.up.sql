-- create "customers" table
CREATE TABLE "customers" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "created_by" uuid NULL, "updated_at" timestamptz NULL, "updated_by" uuid NULL, "deleted_at" timestamptz NULL, "deleted_by" uuid NULL, "data_type_id" uuid NULL, "data_type_slug" character varying NULL, "data" jsonb NULL, PRIMARY KEY ("id"));
-- create "suppliers" table
CREATE TABLE "suppliers" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "created_by" uuid NULL, "updated_at" timestamptz NULL, "updated_by" uuid NULL, "deleted_at" timestamptz NULL, "deleted_by" uuid NULL, "data_type_id" uuid NULL, "data_type_slug" character varying NULL, "data" jsonb NULL, PRIMARY KEY ("id"));
