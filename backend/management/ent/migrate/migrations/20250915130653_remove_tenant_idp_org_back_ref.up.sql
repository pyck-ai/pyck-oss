-- modify "tenants" table
ALTER TABLE "tenants" DROP COLUMN "idp_org_backref";
-- modify "users" table
ALTER TABLE "users" DROP COLUMN "company_id", ADD CONSTRAINT "users_tenants_tenantUsers" FOREIGN KEY ("tenant_id") REFERENCES "tenants" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION;
