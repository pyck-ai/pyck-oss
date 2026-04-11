-- modify "replenishment_orders" table
ALTER TABLE "replenishment_orders"
    ALTER COLUMN "supplier_id" DROP NOT NULL;

-- modify "replenishment_order_items" table
ALTER TABLE "replenishment_order_items"
    RENAME COLUMN "quantity_requested" TO "quantity";
ALTER TABLE "replenishment_order_items"
    DROP COLUMN "quantity_approved";
ALTER TABLE "replenishment_order_items"
    DROP COLUMN "unit_cost";