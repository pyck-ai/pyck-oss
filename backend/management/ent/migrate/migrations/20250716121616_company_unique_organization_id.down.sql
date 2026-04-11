-- reverse: create index "company_organization_id" to table: "companies"
DROP INDEX "company_organization_id";
-- reverse: drop index "companies_organization_id_key" from table: "companies"
CREATE UNIQUE INDEX "companies_organization_id_key" ON "companies" ("organization_id");