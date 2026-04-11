-- Add back columns to replenishment_orders table
ALTER TABLE "replenishment_orders"
    ADD COLUMN "status" character varying NOT NULL DEFAULT 'pending';
ALTER TABLE "replenishment_orders"
    ADD COLUMN "expected_delivery_date" timestamptz NULL;
ALTER TABLE "replenishment_orders"
    ADD COLUMN "priority" character varying NOT NULL DEFAULT 'medium';