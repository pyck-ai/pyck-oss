-- recreate "order-index-values" table
CREATE TABLE "order-index-values" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "slot" character varying NOT NULL, "value" character varying NOT NULL, "order_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "order-index-values_orders_indexValues" FOREIGN KEY ("order_id") REFERENCES "orders" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
CREATE UNIQUE INDEX "orderindexvalue_order_id_slot_value" ON "order-index-values" ("order_id", "slot", "value");
CREATE INDEX "orderindexvalue_tenant_id_slot_value" ON "order-index-values" ("tenant_id", "slot", "value");
