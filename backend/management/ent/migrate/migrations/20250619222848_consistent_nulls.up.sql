-- We back-fill the zero-UUIDs here in order to fulfill the NOT NULL
-- constraint. However, this UPDATE query should not have any matches
-- in practice, as the corresponding schema always enforces a value
-- during the create-hook.
-- See https://github.com/pyck-ai/pyck/issues/175

-- modify "companies" table
UPDATE "companies" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "companies" ALTER COLUMN "created_by" SET NOT NULL;

-- modify "datatypes" table
DROP INDEX "datatype_tenant_id_slug_deleted_at";
UPDATE "datatypes" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "datatypes" ALTER COLUMN "created_by" SET NOT NULL;
ALTER TABLE "datatypes" ALTER COLUMN "description" DROP NOT NULL;
ALTER TABLE "datatypes" ALTER COLUMN "description" DROP DEFAULT;
ALTER TABLE "datatypes" ALTER COLUMN "frontend_schema" DROP DEFAULT;
CREATE UNIQUE INDEX "datatype_tenant_id_slug" ON "datatypes" ("tenant_id", "slug") WHERE (deleted_at IS NULL);

-- modify "events" table
UPDATE "events" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "events" ALTER COLUMN "created_by" SET NOT NULL;

-- modify "users" table
DROP INDEX "user_idp_id_tenant_id_deleted_at";
DROP INDEX "user_username_email_company_id_deleted_at";
UPDATE "users" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "users" ALTER COLUMN "created_by" SET NOT NULL;
CREATE UNIQUE INDEX "user_idp_id_tenant_id" ON "users" ("idp_id", "tenant_id") WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX "user_username_email_company_id" ON "users" ("username", "email", "company_id") WHERE (deleted_at IS NULL);
