-- modify "stocks" table
ALTER TABLE "stocks" ADD COLUMN "version" bigint NOT NULL DEFAULT 0;
-- backfill: re-number existing rows per (tenant, repo, item) group so the
-- unique index below can be created. Uses created_at as the partition order
-- with id as a tiebreaker since created_at ties are possible at microsecond
-- resolution.
UPDATE stocks SET version = sub.rn - 1
FROM (
  SELECT id, ROW_NUMBER() OVER (
    PARTITION BY tenant_id, repository_id, item_id ORDER BY created_at, id
  ) AS rn FROM stocks
) sub
WHERE stocks.id = sub.id;
-- create index "stock_tenant_id_repository_id_item_id_version" to table: "stocks"
CREATE UNIQUE INDEX "stock_tenant_id_repository_id_item_id_version" ON "stocks" ("tenant_id", "repository_id", "item_id", "version");
