-- create "companies" table
CREATE TABLE "companies" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "created_by" uuid NULL, "updated_at" timestamptz NULL, "updated_by" uuid NULL, "deleted_at" timestamptz NULL, "deleted_by" uuid NULL, "name" character varying NOT NULL, "organization_id" character varying NOT NULL, "tenant_id" uuid NOT NULL, PRIMARY KEY ("id"));
-- create index "companies_organization_id_key" to table: "companies"
CREATE UNIQUE INDEX "companies_organization_id_key" ON "companies" ("organization_id");
-- create index "companies_tenant_id_key" to table: "companies"
CREATE UNIQUE INDEX "companies_tenant_id_key" ON "companies" ("tenant_id");
-- create "datatypes" table
CREATE TABLE "datatypes" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "created_by" uuid NULL, "updated_at" timestamptz NULL, "updated_by" uuid NULL, "deleted_at" timestamptz NULL, "deleted_by" uuid NULL, "name" character varying NOT NULL DEFAULT '', "slug" character varying NULL, "description" character varying NOT NULL DEFAULT '', "json_schema" character varying NOT NULL, "frontend_schema" character varying NULL DEFAULT '', "default" boolean NOT NULL DEFAULT false, "entity" character varying NOT NULL, PRIMARY KEY ("id"));
-- create index "datatype_tenant_id_slug_deleted_at" to table: "datatypes"
CREATE UNIQUE INDEX "datatype_tenant_id_slug_deleted_at" ON "datatypes" ("tenant_id", "slug", "deleted_at");
-- create "events" table
CREATE TABLE "events" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "created_by" uuid NULL, "updated_at" timestamptz NULL, "updated_by" uuid NULL, "deleted_at" timestamptz NULL, "deleted_by" uuid NULL, "topic" character varying NOT NULL, "name" character varying NOT NULL DEFAULT '', "description" character varying NOT NULL DEFAULT '', "example" jsonb NULL, PRIMARY KEY ("id"));
-- create index "events_topic_key" to table: "events"
CREATE UNIQUE INDEX "events_topic_key" ON "events" ("topic");
-- create "users" table
CREATE TABLE "users" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "created_by" uuid NULL, "updated_at" timestamptz NULL, "updated_by" uuid NULL, "deleted_at" timestamptz NULL, "deleted_by" uuid NULL, "idp_id" character varying NOT NULL, "username" character varying NOT NULL, "email" character varying NOT NULL, "first_name" character varying NOT NULL, "last_name" character varying NOT NULL, "is_admin" boolean NOT NULL DEFAULT false, "roles" jsonb NOT NULL, "tenant_id" uuid NOT NULL, "company_id" uuid NULL, PRIMARY KEY ("id"), CONSTRAINT "users_companies_companyUsers" FOREIGN KEY ("company_id") REFERENCES "companies" ("id") ON UPDATE NO ACTION ON DELETE SET NULL);
-- create index "user_idp_id_tenant_id_deleted_at" to table: "users"
CREATE UNIQUE INDEX "user_idp_id_tenant_id_deleted_at" ON "users" ("idp_id", "tenant_id", "deleted_at");
-- create index "user_username_email_company_id_deleted_at" to table: "users"
CREATE UNIQUE INDEX "user_username_email_company_id_deleted_at" ON "users" ("username", "email", "company_id", "deleted_at");
