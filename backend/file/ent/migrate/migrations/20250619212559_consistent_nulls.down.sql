-- reverse: create index "file_tenant_id_refid_name" to table: "files"
DROP INDEX "file_tenant_id_refid_name";
-- reverse: modify "files" table
ALTER TABLE "files" ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: drop index "file_tenant_id_refid_name_deleted_at" from table: "files"
CREATE UNIQUE INDEX "file_tenant_id_refid_name_deleted_at" ON "files" ("tenant_id", "refid", "name", "deleted_at");