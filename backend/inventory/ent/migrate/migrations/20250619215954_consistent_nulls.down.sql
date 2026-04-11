-- reverse: modify "transactions" table
ALTER TABLE "transactions" ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: modify "stocks" table
ALTER TABLE "stocks" ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: modify "repository_movements" table
ALTER TABLE "repository_movements" ALTER COLUMN "position" DROP NOT NULL, ALTER COLUMN "executed" DROP NOT NULL, ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: create index "repository_tenant_id_name" to table: "repositories"
DROP INDEX "repository_tenant_id_name";
-- reverse: modify "repositories" table
ALTER TABLE "repositories" ALTER COLUMN "virtual_repo" DROP NOT NULL, ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: drop index "repository_tenant_id_name_deleted_at" from table: "repositories"
CREATE UNIQUE INDEX "repository_tenant_id_name_deleted_at" ON "repositories" ("tenant_id", "name", "deleted_at");
-- reverse: create index "item_tenant_id_sku" to table: "items"
DROP INDEX "item_tenant_id_sku";
-- reverse: modify "items" table
ALTER TABLE "items" ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: drop index "item_tenant_id_sku_deleted_at" from table: "items"
CREATE UNIQUE INDEX "item_tenant_id_sku_deleted_at" ON "items" ("tenant_id", "sku", "deleted_at");
-- reverse: create index "itemset_tenant_id_sku" to table: "item_sets"
DROP INDEX "itemset_tenant_id_sku";
-- reverse: modify "item_sets" table
ALTER TABLE "item_sets" ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: drop index "itemset_tenant_id_sku_deleted_at" from table: "item_sets"
CREATE UNIQUE INDEX "itemset_tenant_id_sku_deleted_at" ON "item_sets" ("tenant_id", "sku", "deleted_at");
-- reverse: modify "item_movements" table
ALTER TABLE "item_movements" ALTER COLUMN "position" DROP NOT NULL, ALTER COLUMN "executed" DROP NOT NULL, ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: modify "collection_movements" table
ALTER TABLE "collection_movements" ALTER COLUMN "created_by" DROP NOT NULL;
