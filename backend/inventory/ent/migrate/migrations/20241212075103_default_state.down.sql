-- reverse: create "transactions" table
DROP TABLE "transactions";
-- reverse: create "stocks" table
DROP TABLE "stocks";
-- reverse: create "repository_movements" table
DROP TABLE "repository_movements";
-- reverse: create "item_set_items" table
DROP TABLE "item_set_items";
-- reverse: create index "itemset_tenant_id_sku_deleted_at" to table: "item_sets"
DROP INDEX "itemset_tenant_id_sku_deleted_at";
-- reverse: create "item_sets" table
DROP TABLE "item_sets";
-- reverse: create "item_movements" table
DROP TABLE "item_movements";
-- reverse: create index "repository_tenant_id_name_deleted_at" to table: "repositories"
DROP INDEX "repository_tenant_id_name_deleted_at";
-- reverse: create "repositories" table
DROP TABLE "repositories";
-- reverse: create index "item_tenant_id_sku_deleted_at" to table: "items"
DROP INDEX "item_tenant_id_sku_deleted_at";
-- reverse: create "items" table
DROP TABLE "items";
-- reverse: create "collection_movements" table
DROP TABLE "collection_movements";
