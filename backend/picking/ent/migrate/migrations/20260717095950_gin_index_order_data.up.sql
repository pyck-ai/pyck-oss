-- create index "order_data" to table: "orders"
CREATE INDEX "order_data" ON "orders" USING gin ("data");
