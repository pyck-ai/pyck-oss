-- reverse: modify "inbounds" table
ALTER TABLE "inbounds" ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: modify "inbound-items" table
ALTER TABLE "inbound-items" ALTER COLUMN "created_by" DROP NOT NULL;
