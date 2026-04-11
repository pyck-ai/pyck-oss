-- Composite index to optimize NOT EXISTS "latest per group" queries on stocks.
CREATE INDEX IF NOT EXISTS idx_stocks_tenant_repo_item_created
    ON inventory.stocks (tenant_id, repository_id, item_id, created_at);
