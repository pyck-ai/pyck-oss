-- reverse: modify "orders" table
ALTER TABLE "orders" ALTER COLUMN "created_by" DROP NOT NULL;
-- reverse: modify "order-items" table
ALTER TABLE "order-items" ALTER COLUMN "created_by" DROP NOT NULL;
