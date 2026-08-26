-- Signature must match the up migration exactly so PostgreSQL locates the overload.
DROP FUNCTION IF EXISTS inventory.load_stock_rebase_snapshot(uuid, jsonb);
