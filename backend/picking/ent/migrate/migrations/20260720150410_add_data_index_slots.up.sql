-- modify "orders" table
ALTER TABLE "orders" ADD COLUMN "data_ix_text1" character varying NULL, ADD COLUMN "data_ix_text2" character varying NULL, ADD COLUMN "data_ix_text3" character varying NULL, ADD COLUMN "data_ix_text4" character varying NULL, ADD COLUMN "data_ix_numeric1" double precision NULL, ADD COLUMN "data_ix_numeric2" double precision NULL, ADD COLUMN "data_ix_bool1" boolean NULL, ADD COLUMN "data_ix_bool2" boolean NULL, ADD COLUMN "data_ix_list1" jsonb NULL, ADD COLUMN "data_ix_list2" jsonb NULL;
-- create index "order_data_ix_list1" to table: "orders"
CREATE INDEX "order_data_ix_list1" ON "orders" USING gin ("data_ix_list1");
-- create index "order_data_ix_list2" to table: "orders"
CREATE INDEX "order_data_ix_list2" ON "orders" USING gin ("data_ix_list2");
-- create index "order_tenant_id_data_ix_bool1" to table: "orders"
CREATE INDEX "order_tenant_id_data_ix_bool1" ON "orders" ("tenant_id", "data_ix_bool1") WHERE (data_ix_bool1 IS NOT NULL);
-- create index "order_tenant_id_data_ix_bool2" to table: "orders"
CREATE INDEX "order_tenant_id_data_ix_bool2" ON "orders" ("tenant_id", "data_ix_bool2") WHERE (data_ix_bool2 IS NOT NULL);
-- create index "order_tenant_id_data_ix_numeric1" to table: "orders"
CREATE INDEX "order_tenant_id_data_ix_numeric1" ON "orders" ("tenant_id", "data_ix_numeric1") WHERE (data_ix_numeric1 IS NOT NULL);
-- create index "order_tenant_id_data_ix_numeric2" to table: "orders"
CREATE INDEX "order_tenant_id_data_ix_numeric2" ON "orders" ("tenant_id", "data_ix_numeric2") WHERE (data_ix_numeric2 IS NOT NULL);
-- create index "order_tenant_id_data_ix_text1" to table: "orders"
CREATE INDEX "order_tenant_id_data_ix_text1" ON "orders" ("tenant_id", "data_ix_text1") WHERE (data_ix_text1 IS NOT NULL);
-- create index "order_tenant_id_data_ix_text2" to table: "orders"
CREATE INDEX "order_tenant_id_data_ix_text2" ON "orders" ("tenant_id", "data_ix_text2") WHERE (data_ix_text2 IS NOT NULL);
-- create index "order_tenant_id_data_ix_text3" to table: "orders"
CREATE INDEX "order_tenant_id_data_ix_text3" ON "orders" ("tenant_id", "data_ix_text3") WHERE (data_ix_text3 IS NOT NULL);
-- create index "order_tenant_id_data_ix_text4" to table: "orders"
CREATE INDEX "order_tenant_id_data_ix_text4" ON "orders" ("tenant_id", "data_ix_text4") WHERE (data_ix_text4 IS NOT NULL);
