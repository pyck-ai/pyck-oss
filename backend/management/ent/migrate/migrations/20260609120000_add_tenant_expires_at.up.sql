-- modify "tenants" table
ALTER TABLE "tenants" ADD COLUMN "expires_at" timestamptz NULL;
-- create index "tenant_expires_at" to table: "tenants"
CREATE INDEX "tenant_expires_at" ON "tenants" ("expires_at") WHERE ((expires_at IS NOT NULL) AND (deleted_at IS NULL));
