-- reverse: create index "company_tenant_id" to table: "companies"
DROP INDEX "company_tenant_id";
-- reverse: drop index "companies_tenant_id_key" from table: "companies"
CREATE UNIQUE INDEX "companies_tenant_id_key" ON "companies" ("tenant_id");
