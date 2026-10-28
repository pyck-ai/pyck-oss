-- add "name_key" nullable first, backfill it, then enforce NOT NULL and the
-- unique index over every row, soft-deleted ones included.
--
-- name_key is the name folded the way Zitadel folds an org name
-- (strings.ToLower; types.TenantNameKey writes it for every new row). The
-- backfill folds with the builtin provider's pg_c_utf8 collation, which
-- applies Unicode simple case mapping independent of the server's libc
-- locale; lower() under a libc locale leaves code points such as the
-- circled letters (U+24B6..) unfolded.
ALTER TABLE "tenants" ADD COLUMN "name_key" character varying;

UPDATE "tenants" SET "name_key" = lower("name" COLLATE pg_c_utf8);

-- Rows that fold to one key predate the index and cannot all keep it. The
-- live row, then the oldest, keeps the plain key, so a new registration of
-- that name is refused as taken; every other row's key gets "#<id>"
-- appended, which no folded name produces. Names are left as they are, so
-- no tenant changes its name and restoreTenant still works on every row.
UPDATE "tenants" AS t
SET "name_key" = t."name_key" || '#' || t."id"
FROM (
  SELECT "id", row_number() OVER (
    PARTITION BY "name_key"
    ORDER BY ("deleted_at" IS NOT NULL), "created_at", "id"
  ) AS "rank"
  FROM "tenants"
) AS d
WHERE d."id" = t."id" AND d."rank" > 1;

-- The two folds differ only on capitals newer than the server's Unicode
-- tables, which Postgres leaves as they are: 55 on PostgreSQL 17, 28 on 18
-- (U+1C89, U+A7CB, U+A7CC, U+A7CE, U+A7D2, U+A7D4, U+A7DA, U+A7DC,
-- U+10D50..U+10D65, U+16EA0..U+16EB8). A row whose backfilled key still
-- holds one keeps a key the Go code never writes, so a registration of the
-- identical name passes the index and ends as an error row once Zitadel
-- refuses the org name. That is the whole cost, so the migration names
-- such rows in a warning and goes on rather than failing the deploy.
DO $$
DECLARE
  unfolded text;
BEGIN
  SELECT string_agg(quote_literal("name"), ', ' ORDER BY "name") INTO unfolded
  FROM "tenants"
  WHERE "name_key" ~ '[ᲉꟋꟌ꟎꟒꟔ꟚꟜ\U00010D50-\U00010D65\U00016EA0-\U00016EB8]';
  IF unfolded IS NOT NULL THEN
    RAISE WARNING 'tenant names hold a capital this server cannot lowercase, their name_key is not the Go fold: %', unfolded;
  END IF;
END
$$;

ALTER TABLE "tenants" ALTER COLUMN "name_key" SET NOT NULL;

-- create index "tenant_name_key" to table: "tenants"
CREATE UNIQUE INDEX "tenant_name_key" ON "tenants" ("name_key");
