-- reverse: create index "order_tenant_id_data_ix_text4" to table: "orders"
DROP INDEX "order_tenant_id_data_ix_text4";
-- reverse: create index "order_tenant_id_data_ix_text3" to table: "orders"
DROP INDEX "order_tenant_id_data_ix_text3";
-- reverse: create index "order_tenant_id_data_ix_text2" to table: "orders"
DROP INDEX "order_tenant_id_data_ix_text2";
-- reverse: create index "order_tenant_id_data_ix_text1" to table: "orders"
DROP INDEX "order_tenant_id_data_ix_text1";
-- reverse: create index "order_tenant_id_data_ix_numeric2" to table: "orders"
DROP INDEX "order_tenant_id_data_ix_numeric2";
-- reverse: create index "order_tenant_id_data_ix_numeric1" to table: "orders"
DROP INDEX "order_tenant_id_data_ix_numeric1";
-- reverse: create index "order_tenant_id_data_ix_bool2" to table: "orders"
DROP INDEX "order_tenant_id_data_ix_bool2";
-- reverse: create index "order_tenant_id_data_ix_bool1" to table: "orders"
DROP INDEX "order_tenant_id_data_ix_bool1";
-- reverse: create index "order_data_ix_list2" to table: "orders"
DROP INDEX "order_data_ix_list2";
-- reverse: create index "order_data_ix_list1" to table: "orders"
DROP INDEX "order_data_ix_list1";
-- reverse: modify "orders" table
ALTER TABLE "orders" DROP COLUMN "data_ix_list2", DROP COLUMN "data_ix_list1", DROP COLUMN "data_ix_bool2", DROP COLUMN "data_ix_bool1", DROP COLUMN "data_ix_numeric2", DROP COLUMN "data_ix_numeric1", DROP COLUMN "data_ix_text4", DROP COLUMN "data_ix_text3", DROP COLUMN "data_ix_text2", DROP COLUMN "data_ix_text1";
