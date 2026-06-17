-- create index "itemmovement_collection_id_position" to table: "item_movements"
CREATE INDEX "itemmovement_collection_id_position" ON "item_movements" ("collection_id", "position");
