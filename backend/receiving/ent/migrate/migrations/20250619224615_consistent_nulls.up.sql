-- We back-fill the zero-UUIDs here in order to fulfill the NOT NULL
-- constraint. However, this UPDATE query should not have any matches
-- in practice, as the corresponding schema always enforces a value
-- during the create-hook.
-- See https://github.com/pyck-ai/pyck/issues/175

-- modify "inbound-items" table
UPDATE "inbound-items" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "inbound-items" ALTER COLUMN "created_by" SET NOT NULL;

-- modify "inbounds" table
UPDATE "inbounds" SET "created_by" = '00000000-0000-0000-0000-000000000000' WHERE "created_by" IS NULL;
ALTER TABLE "inbounds" ALTER COLUMN "created_by" SET NOT NULL;
