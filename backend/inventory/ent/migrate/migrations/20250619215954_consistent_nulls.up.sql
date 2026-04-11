-- We back-fill the zero-UUIDs here in order to fulfill the NOT NULL
-- constraint. However, this UPDATE query should not have any matches
-- in practice, as the corresponding schema always enforces a value
-- during the create-hook.
-- See https://github.com/pyck-ai/pyck/issues/175

-- modify "collection_movements" table
UPDATE "collection_movements" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "collection_movements" ALTER COLUMN "created_by" SET NOT NULL;

-- modify "item_movements" table
UPDATE "item_movements" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "item_movements" ALTER COLUMN "created_by" SET NOT NULL;
UPDATE "item_movements" SET "executed" = false WHERE "executed" IS NULL;
ALTER TABLE "item_movements" ALTER COLUMN "executed" SET NOT NULL;
UPDATE "item_movements" SET "position" = 0 WHERE "position" IS NULL;
ALTER TABLE "item_movements" ALTER COLUMN "position" SET NOT NULL;

-- modify "item_sets" table
DROP INDEX "itemset_tenant_id_sku_deleted_at";
UPDATE "item_sets" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "item_sets" ALTER COLUMN "created_by" SET NOT NULL;
CREATE UNIQUE INDEX "itemset_tenant_id_sku" ON "item_sets" ("tenant_id", "sku") WHERE (deleted_at IS NULL);

-- modify "items" table
DROP INDEX "item_tenant_id_sku_deleted_at";
UPDATE "items" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "items" ALTER COLUMN "created_by" SET NOT NULL;
CREATE UNIQUE INDEX "item_tenant_id_sku" ON "items" ("tenant_id", "sku") WHERE (deleted_at IS NULL);

-- modify "repositories" table
DROP INDEX "repository_tenant_id_name_deleted_at";
UPDATE "repositories" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "repositories" ALTER COLUMN "created_by" SET NOT NULL;
UPDATE "repositories" SET "virtual_repo" = false WHERE "virtual_repo" IS NULL;
ALTER TABLE "repositories" ALTER COLUMN "virtual_repo" SET NOT NULL;
CREATE UNIQUE INDEX "repository_tenant_id_name" ON "repositories" ("tenant_id", "name") WHERE (deleted_at IS NULL);

-- modify "repository_movements" table
UPDATE "repository_movements" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "repository_movements" ALTER COLUMN "created_by" SET NOT NULL;
UPDATE "repository_movements" SET "executed" = false WHERE "executed" IS NULL;
ALTER TABLE "repository_movements" ALTER COLUMN "executed" SET NOT NULL;
UPDATE "repository_movements" SET "position" = 0 WHERE "position" IS NULL;
ALTER TABLE "repository_movements" ALTER COLUMN "position" SET NOT NULL;

-- modify "stocks" table
UPDATE "stocks" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "stocks" ALTER COLUMN "created_by" SET NOT NULL;

-- modify "transactions" table
UPDATE "transactions" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "transactions" ALTER COLUMN "created_by" SET NOT NULL;
