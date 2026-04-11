-- drop index "companies_organization_id_key" from table: "companies"
DROP INDEX "companies_organization_id_key";
-- create index "company_organization_id" to table: "companies"
CREATE UNIQUE INDEX "company_organization_id" ON "companies" ("organization_id") WHERE (deleted_at IS NULL);
