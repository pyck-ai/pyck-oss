-- reverse: create index "file_tenant_id_refid_name_deleted_at" to table: "files"
DROP INDEX "file_tenant_id_refid_name_deleted_at";
-- reverse: create index "file_refid" to table: "files"
DROP INDEX "file_refid";
-- reverse: create "files" table
DROP TABLE "files";
