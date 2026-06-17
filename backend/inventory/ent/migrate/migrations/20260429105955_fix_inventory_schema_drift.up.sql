-- drop index "idx_event_outbox_correlation" from table: "event_outbox"
DROP INDEX "idx_event_outbox_correlation";
-- drop index "idx_event_outbox_polling_v2" from table: "event_outbox"
DROP INDEX "idx_event_outbox_polling_v2";
-- drop index "idx_event_outbox_tenant" from table: "event_outbox"
DROP INDEX "idx_event_outbox_tenant";
-- drop index "idx_event_outbox_user_created" from table: "event_outbox"
DROP INDEX "idx_event_outbox_user_created";
-- modify "event_outbox" table
ALTER TABLE "event_outbox" ALTER COLUMN "created_at" DROP DEFAULT, ALTER COLUMN "retry_count" TYPE bigint;
-- create index "entityeventsoutbox_correlation_id_created_at" to table: "event_outbox"
CREATE INDEX "entityeventsoutbox_correlation_id_created_at" ON "event_outbox" ("correlation_id", "created_at") WHERE ((published_at IS NULL) AND (dead_at IS NULL));
-- create index "entityeventsoutbox_next_retry_at_correlation_id_created_at" to table: "event_outbox"
CREATE INDEX "entityeventsoutbox_next_retry_at_correlation_id_created_at" ON "event_outbox" ("next_retry_at", "correlation_id", "created_at") WHERE ((published_at IS NULL) AND (dead_at IS NULL));
-- create index "entityeventsoutbox_tenant_id_created_at" to table: "event_outbox"
CREATE INDEX "entityeventsoutbox_tenant_id_created_at" ON "event_outbox" ("tenant_id", "created_at") WHERE ((published_at IS NULL) AND (dead_at IS NULL));
-- create index "entityeventsoutbox_user_id_created_at" to table: "event_outbox"
CREATE INDEX "entityeventsoutbox_user_id_created_at" ON "event_outbox" ("user_id", "created_at");
-- set comment to table: "event_outbox"
COMMENT ON TABLE "event_outbox" IS '';
-- set comment to column: "correlation_id" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."correlation_id" IS '';
-- set comment to column: "with_reply" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."with_reply" IS '';
-- set comment to column: "retry_count" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."retry_count" IS '';
-- set comment to column: "dead_at" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."dead_at" IS '';
-- set comment to column: "next_retry_at" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."next_retry_at" IS '';
-- set comment to column: "entity_type" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."entity_type" IS '';
-- set comment to column: "entity_id" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."entity_id" IS '';
-- set comment to column: "tenant_id" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."tenant_id" IS '';
-- modify "repositories" table
ALTER TABLE "repositories" ADD CONSTRAINT "repositories_repositories_children" FOREIGN KEY ("parent_id") REFERENCES "repositories" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- drop index "idx_stocks_tenant_repo_item_created" from table: "stocks"
DROP INDEX "idx_stocks_tenant_repo_item_created";
-- create index "stock_tenant_id_repository_id_item_id_created_at" to table: "stocks"
CREATE INDEX "stock_tenant_id_repository_id_item_id_created_at" ON "stocks" ("tenant_id", "repository_id", "item_id", "created_at");
