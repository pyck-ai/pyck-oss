-- We back-fill the zero-UUIDs here in order to fulfill the NOT NULL
-- constraint. However, this UPDATE query should not have any matches
-- in practice, as the corresponding schema always enforces a value
-- during the create-hook.
-- See https://github.com/pyck-ai/pyck/issues/175

-- modify "customers" table
UPDATE "customers" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "customers" ALTER COLUMN "created_by" SET NOT NULL;

-- modify "suppliers" table
UPDATE "suppliers" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "suppliers" ALTER COLUMN "created_by" SET NOT NULL;
