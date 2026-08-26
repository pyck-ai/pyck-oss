-- create index "orderitems_order_id" to table: "order-items"
CREATE INDEX "orderitems_order_id" ON "order-items" ("order_id") WHERE (deleted_at IS NULL);
