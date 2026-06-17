-- create index "itemmovement_executed" to table: "item_movements"
CREATE INDEX "itemmovement_executed" ON "item_movements" ("executed");
-- create index "itemmovement_from_id" to table: "item_movements"
CREATE INDEX "itemmovement_from_id" ON "item_movements" ("from_id");
-- create index "itemmovement_item_id" to table: "item_movements"
CREATE INDEX "itemmovement_item_id" ON "item_movements" ("item_id");
