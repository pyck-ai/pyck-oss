-- reverse: create index "stock_repository_id_item_id_version" to table: "stocks"
DROP INDEX IF EXISTS "stock_repository_id_item_id_version";
-- reverse: drop index "stock_repository_id_item_id_created_at" from table: "stocks"
CREATE INDEX IF NOT EXISTS "stock_repository_id_item_id_created_at" ON "stocks" ("repository_id", "item_id", "created_at" DESC);
