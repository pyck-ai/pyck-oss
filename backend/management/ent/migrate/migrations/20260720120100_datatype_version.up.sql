-- drop index "datatype_tenant_id_slug" from table: "datatypes"
DROP INDEX "datatype_tenant_id_slug";

-- add "version" nullable first, then backfill #990 version numbers per
-- (tenant_id, slug) family before enforcing NOT NULL + the unique index.
-- ROW_NUMBER numbers each family densely from 1 ordered by age, so pre-#990
-- rows (the old partial unique index allowed multiple soft-deleted rows per
-- family) don't all collapse onto version 1 and collide on the new
-- non-partial (tenant_id, slug, version) unique index. The DEFAULT 1 is for
-- new rows only; createDataType assigns MAX(version)+1 at runtime.
ALTER TABLE "datatypes" ADD COLUMN "version" bigint;

UPDATE "datatypes" d SET "version" = sub.rn
FROM (
    SELECT id, ROW_NUMBER() OVER (
        PARTITION BY tenant_id, slug ORDER BY created_at, id
    ) AS rn
    FROM "datatypes"
) sub
WHERE d.id = sub.id;

ALTER TABLE "datatypes" ALTER COLUMN "version" SET NOT NULL;

ALTER TABLE "datatypes" ALTER COLUMN "version" SET DEFAULT 1;

-- create index "datatype_tenant_id_slug_version_desc" to table: "datatypes"
CREATE INDEX "datatype_tenant_id_slug_version_desc" ON "datatypes" ("tenant_id", "slug", "version" DESC) WHERE (deleted_at IS NULL);

-- create index "datatype_tenant_id_slug_version_uniq" to table: "datatypes"
CREATE UNIQUE INDEX "datatype_tenant_id_slug_version_uniq" ON "datatypes" ("tenant_id", "slug", "version");
