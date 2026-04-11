-- create "keyvalues" table
CREATE TABLE "keyvalues" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "user_id" uuid NULL, "data_type_id" uuid NULL, "data_type_slug" character varying NULL, "data" jsonb NULL, "created_at" timestamptz NOT NULL, "created_by" uuid NOT NULL, "updated_at" timestamptz NULL, "updated_by" uuid NULL, "deleted_at" timestamptz NULL, "deleted_by" uuid NULL, "name" character varying NOT NULL DEFAULT '', PRIMARY KEY ("id"));
-- create index "keyvalue_tenant_id_user_id_name" to table: "keyvalues"
CREATE UNIQUE INDEX "keyvalue_tenant_id_user_id_name" ON "keyvalues" ("tenant_id", "user_id", "name") WHERE (deleted_at IS NULL);
