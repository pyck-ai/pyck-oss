-- reverse: modify "users" table
ALTER TABLE "users" DROP CONSTRAINT "users_tenants_tenantUsers", ADD COLUMN "company_id" uuid NULL;
-- reverse: modify "tenants" table
ALTER TABLE "tenants" ADD COLUMN "idp_org_backref" uuid NOT NULL;
