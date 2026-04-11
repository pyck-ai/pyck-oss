-- reverse: create index "user_username_email_company_id" to table: "users"
DROP INDEX "user_username_email_company_id";
-- reverse: create index "user_idp_id_tenant_id" to table: "users"
DROP INDEX "user_idp_id_tenant_id";
-- reverse: modify "users" table
ALTER TABLE "users" ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: drop index "user_username_email_company_id_deleted_at" from table: "users"
CREATE UNIQUE INDEX "user_username_email_company_id_deleted_at" ON "users" ("username", "email", "company_id", "deleted_at");
-- reverse: drop index "user_idp_id_tenant_id_deleted_at" from table: "users"
CREATE UNIQUE INDEX "user_idp_id_tenant_id_deleted_at" ON "users" ("idp_id", "tenant_id", "deleted_at");
-- reverse: modify "events" table
ALTER TABLE "events" ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: create index "datatype_tenant_id_slug" to table: "datatypes"
DROP INDEX "datatype_tenant_id_slug";
-- reverse: modify "datatypes" table
ALTER TABLE "datatypes" ALTER COLUMN "frontend_schema" SET DEFAULT '', ALTER COLUMN "description" SET NOT NULL, ALTER COLUMN "description" SET DEFAULT '', ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: drop index "datatype_tenant_id_slug_deleted_at" from table: "datatypes"
CREATE UNIQUE INDEX "datatype_tenant_id_slug_deleted_at" ON "datatypes" ("tenant_id", "slug", "deleted_at");
-- reverse: modify "companies" table
ALTER TABLE "companies" ALTER COLUMN "created_by" DROP NOT NULL;
