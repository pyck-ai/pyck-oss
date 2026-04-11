-- reverse: modify "replenishment_order_items" table
ALTER TABLE "replenishment_order_items"
    ADD COLUMN "unit_cost" double precision NULL;
ALTER TABLE "replenishment_order_items"
    ADD COLUMN "quantity_approved" bigint NULL;
ALTER TABLE "replenishment_order_items"
    RENAME COLUMN "quantity" TO "quantity_requested";

-- reverse: modify "replenishment_orders" table
ALTER TABLE "replenishment_orders"
    ALTER COLUMN "supplier_id" SET NOT NULL;