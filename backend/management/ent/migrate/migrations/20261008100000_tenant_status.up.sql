-- modify "tenants" table
ALTER TABLE "tenants" ADD COLUMN "status" character varying NOT NULL DEFAULT 'done';
