-- create index "repositorymovement_collection_id_position" to table: "repository_movements"
CREATE INDEX "repositorymovement_collection_id_position" ON "repository_movements" ("collection_id", "position");
-- create index "repositorymovement_executed" to table: "repository_movements"
CREATE INDEX "repositorymovement_executed" ON "repository_movements" ("executed");
-- create index "repositorymovement_from_id" to table: "repository_movements"
CREATE INDEX "repositorymovement_from_id" ON "repository_movements" ("from_id");
-- create index "repositorymovement_repository_id" to table: "repository_movements"
CREATE INDEX "repositorymovement_repository_id" ON "repository_movements" ("repository_id");
