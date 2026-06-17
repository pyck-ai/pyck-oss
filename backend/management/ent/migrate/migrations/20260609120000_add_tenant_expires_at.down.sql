-- reverse: create index "tenant_expires_at" to table: "tenants"
DROP INDEX "tenant_expires_at";
-- reverse: modify "tenants" table
ALTER TABLE "tenants" DROP COLUMN "expires_at";
