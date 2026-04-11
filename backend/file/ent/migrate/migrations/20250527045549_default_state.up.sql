-- create "files" table
CREATE TABLE "files" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "created_by" uuid NULL, "updated_at" timestamptz NULL, "updated_by" uuid NULL, "deleted_at" timestamptz NULL, "deleted_by" uuid NULL, "data_type_id" uuid NULL, "data_type_slug" character varying NULL, "data" jsonb NULL, "refid" uuid NOT NULL, "reftype" character varying NOT NULL, "description" character varying NULL, "name" character varying NOT NULL, "size" bigint NOT NULL, "content_type" character varying NOT NULL, PRIMARY KEY ("id"));
-- create index "file_refid" to table: "files"
CREATE INDEX "file_refid" ON "files" ("refid");
-- create index "file_tenant_id_refid_name_deleted_at" to table: "files"
CREATE UNIQUE INDEX "file_tenant_id_refid_name_deleted_at" ON "files" ("tenant_id", "refid", "name", "deleted_at");
