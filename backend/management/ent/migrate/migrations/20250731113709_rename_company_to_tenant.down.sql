-- reverse: create index "user_username_email_tenant_id" to table: "users"
DROP INDEX "user_username_email_tenant_id";
-- reverse: modify "users" table
ALTER TABLE "users" ADD CONSTRAINT "users_companies_companyUsers" FOREIGN KEY ("company_id") REFERENCES "tenants" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- reverse: drop index "user_username_email_company_id" from table: "users"
CREATE UNIQUE INDEX "user_username_email_company_id" ON "users" ("username", "email", "company_id") WHERE (deleted_at IS NULL);

-- drop new tenant indexes if they exist
DROP INDEX IF EXISTS tenant_idp_org_ref;
DROP INDEX IF EXISTS tenant_idp_org_backref;

-- rename columns back to company schema
ALTER TABLE tenants RENAME COLUMN idp_org_ref TO organization_id;
ALTER TABLE tenants ALTER COLUMN organization_id TYPE varchar;
ALTER TABLE tenants RENAME COLUMN idp_org_backref TO tenant_id;
ALTER TABLE tenants ALTER COLUMN tenant_id TYPE uuid;
ALTER TABLE tenants ALTER COLUMN organization_id SET NOT NULL;
ALTER TABLE tenants ALTER COLUMN tenant_id SET NOT NULL;

-- rename table back to companies
ALTER TABLE tenants RENAME TO companies;

-- recreate old company indexes
CREATE UNIQUE INDEX company_organization_id ON companies (organization_id);
CREATE UNIQUE INDEX company_tenant_id ON companies (tenant_id);