-- reverse: create index "user_username_email_company_id_deleted_at" to table: "users"
DROP INDEX "user_username_email_company_id_deleted_at";
-- reverse: create index "user_idp_id_tenant_id_deleted_at" to table: "users"
DROP INDEX "user_idp_id_tenant_id_deleted_at";
-- reverse: create "users" table
DROP TABLE "users";
-- reverse: create index "events_topic_key" to table: "events"
DROP INDEX "events_topic_key";
-- reverse: create "events" table
DROP TABLE "events";
-- reverse: create index "datatype_tenant_id_slug_deleted_at" to table: "datatypes"
DROP INDEX "datatype_tenant_id_slug_deleted_at";
-- reverse: create "datatypes" table
DROP TABLE "datatypes";
-- reverse: create index "companies_tenant_id_key" to table: "companies"
DROP INDEX "companies_tenant_id_key";
-- reverse: create index "companies_organization_id_key" to table: "companies"
DROP INDEX "companies_organization_id_key";
-- reverse: create "companies" table
DROP TABLE "companies";
