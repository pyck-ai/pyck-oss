-- reverse: modify "tenants" table
ALTER TABLE "tenants" DROP COLUMN "data", DROP COLUMN "data_type_slug", DROP COLUMN "data_type_id";
