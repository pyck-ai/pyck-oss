-- drop index "companies_tenant_id_key" from table: "companies"
DROP INDEX "companies_tenant_id_key";
-- create index "company_tenant_id" to table: "companies"
CREATE UNIQUE INDEX "company_tenant_id" ON "companies" ("tenant_id") WHERE (deleted_at IS NULL);
