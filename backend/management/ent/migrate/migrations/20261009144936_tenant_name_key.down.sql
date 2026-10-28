-- reverse: create index "tenant_name_key" to table: "tenants"
DROP INDEX "tenant_name_key";
-- reverse: modify "tenants" table
ALTER TABLE "tenants" DROP COLUMN "name_key";
