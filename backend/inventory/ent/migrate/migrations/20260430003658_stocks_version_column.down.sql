-- reverse: create index "stock_tenant_id_repository_id_item_id_version" to table: "stocks"
DROP INDEX "stock_tenant_id_repository_id_item_id_version";
-- reverse: modify "stocks" table
ALTER TABLE "stocks" DROP COLUMN "version";
