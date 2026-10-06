-- Serves InventoryItem.itemstocks (an item's current stock rows) and the
-- itemmovementitems / itemtransactions lists (an item's newest rows first).
--
-- Plain CREATE INDEX rather than CONCURRENTLY: the migration framework wraps
-- each migration in a transaction, and inventory runs migrations before
-- opening its listener. IF NOT EXISTS so a manual CONCURRENTLY run on a hot
-- tenant ahead of the deploy does not collide.
CREATE INDEX IF NOT EXISTS "itemmovement_tenant_id_item_id_created_at_id" ON "item_movements" ("tenant_id", "item_id", "created_at" DESC, "id" DESC);
CREATE INDEX IF NOT EXISTS "stock_tenant_id_item_id_repository_id_version" ON "stocks" ("tenant_id", "item_id", "repository_id", "version" DESC);
CREATE INDEX IF NOT EXISTS "transaction_tenant_id_item_id_created_at_id" ON "transactions" ("tenant_id", "item_id", "created_at" DESC, "id" DESC);
