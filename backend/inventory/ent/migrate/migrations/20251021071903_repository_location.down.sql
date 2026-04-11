-- reverse: modify "replenishment_order_items" table
ALTER TABLE "replenishment_order_items" DROP CONSTRAINT "replenishment_order_items_repl_808f61503bde64326e44819f59063668", ALTER COLUMN "created_by" DROP NOT NULL, ADD CONSTRAINT "replenishment_order_items_replenishment_orders_replenishmentOrd" FOREIGN KEY ("replenishment_order_id") REFERENCES "replenishment_orders" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION;
-- reverse: modify "repositories" table
ALTER TABLE "repositories" DROP COLUMN "location_id";
-- reverse: modify "replenishment_orders" table
ALTER TABLE "replenishment_orders" ALTER COLUMN "created_by" DROP NOT NULL;
