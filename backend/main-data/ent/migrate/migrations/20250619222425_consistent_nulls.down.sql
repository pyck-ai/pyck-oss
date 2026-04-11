-- reverse: modify "suppliers" table
ALTER TABLE "suppliers" ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: modify "customers" table
ALTER TABLE "customers" ALTER COLUMN "created_by" DROP NOT NULL;
