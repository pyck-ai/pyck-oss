-- drop old company indexes if they exist
DROP INDEX IF EXISTS "company_organization_id";
DROP INDEX IF EXISTS "company_tenant_id";

-- rename table and columns to migrate company to tenant
ALTER TABLE companies RENAME TO tenants;
ALTER TABLE tenants RENAME COLUMN organization_id TO idp_org_ref;
ALTER TABLE tenants ALTER COLUMN idp_org_ref TYPE varchar;
ALTER TABLE tenants RENAME COLUMN tenant_id TO idp_org_backref;
ALTER TABLE tenants ALTER COLUMN idp_org_backref TYPE uuid;
ALTER TABLE tenants ALTER COLUMN idp_org_ref SET NOT NULL;
ALTER TABLE tenants ALTER COLUMN idp_org_backref SET NOT NULL;

-- create new tenant indexes for unique constraints
CREATE UNIQUE INDEX tenant_idp_org_ref ON tenants (idp_org_ref) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX tenant_idp_org_backref ON tenants (idp_org_backref) WHERE (deleted_at IS NULL);

-- drop index "user_username_email_company_id" from table: "users"
DROP INDEX "user_username_email_company_id";
-- modify "users" table
ALTER TABLE "users" DROP CONSTRAINT "users_companies_companyUsers";
-- create index "user_username_email_tenant_id" to table: "users"
CREATE UNIQUE INDEX "user_username_email_tenant_id" ON "users" ("username", "email", "tenant_id") WHERE (deleted_at IS NULL);
