-- Drop columns from replenishment_orders table
ALTER TABLE "replenishment_orders" DROP COLUMN IF EXISTS "status";
ALTER TABLE "replenishment_orders" DROP COLUMN IF EXISTS "expected_delivery_date";
ALTER TABLE "replenishment_orders" DROP COLUMN IF EXISTS "priority";