-- Replaces the (repository_id, item_id, created_at DESC) index from
-- 20260521135054, flagged there as a temporary workaround to drop once the
-- code-side fix landed. It has: stock reads now pick the current row by version.
--
-- Plain CREATE INDEX rather than CONCURRENTLY: the migration framework wraps
-- each migration in a transaction, and inventory runs migrations before
-- opening its listener. IF [NOT] EXISTS so a manual CONCURRENTLY run on a hot
-- tenant ahead of the deploy does not collide.
DROP INDEX IF EXISTS "stock_repository_id_item_id_created_at";
CREATE INDEX IF NOT EXISTS "stock_repository_id_item_id_version" ON "stocks" ("repository_id", "item_id", "version" DESC);
