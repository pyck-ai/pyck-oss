-- modify "tenants" table
ALTER TABLE "tenants" ADD COLUMN "data_type_id" uuid NULL, ADD COLUMN "data_type_slug" character varying NULL, ADD COLUMN "data" jsonb NULL;
