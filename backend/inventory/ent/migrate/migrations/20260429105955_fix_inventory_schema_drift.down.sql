-- reverse: create index "stock_tenant_id_repository_id_item_id_created_at" to table: "stocks"
DROP INDEX "stock_tenant_id_repository_id_item_id_created_at";
-- reverse: drop index "idx_stocks_tenant_repo_item_created" from table: "stocks"
CREATE INDEX "idx_stocks_tenant_repo_item_created" ON "stocks" ("tenant_id", "repository_id", "item_id", "created_at");
-- reverse: modify "repositories" table
ALTER TABLE "repositories" DROP CONSTRAINT "repositories_repositories_children";
-- reverse: set comment to column: "tenant_id" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."tenant_id" IS 'Tenant UUID extracted from payload for filtering.';
-- reverse: set comment to column: "entity_id" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."entity_id" IS 'Entity UUID extracted from payload for filtering.';
-- reverse: set comment to column: "entity_type" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."entity_type" IS 'Ent schema name (e.g., Item, Location) extracted from payload for filtering.';
-- reverse: set comment to column: "next_retry_at" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."next_retry_at" IS 'Earliest time for next retry. NULL = immediately eligible. Set to NOW() + LEAST(2^retry_count, 3600) seconds on failure.';
-- reverse: set comment to column: "dead_at" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."dead_at" IS 'When set, marks the correlation group as dead for manual cleanup';
-- reverse: set comment to column: "retry_count" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."retry_count" IS 'Number of failed publish attempts, entry skipped after max retries';
-- reverse: set comment to column: "with_reply" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."with_reply" IS 'When true, OutboxHandler waits for workflow IDs in NATS reply';
-- reverse: set comment to column: "correlation_id" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."correlation_id" IS 'Links events across services, used for NATS message deduplication';
-- reverse: set comment to table: "event_outbox"
COMMENT ON TABLE "event_outbox" IS 'Transactional outbox for reliable event publishing to NATS';
-- reverse: create index "entityeventsoutbox_user_id_created_at" to table: "event_outbox"
DROP INDEX "entityeventsoutbox_user_id_created_at";
-- reverse: create index "entityeventsoutbox_tenant_id_created_at" to table: "event_outbox"
DROP INDEX "entityeventsoutbox_tenant_id_created_at";
-- reverse: create index "entityeventsoutbox_next_retry_at_correlation_id_created_at" to table: "event_outbox"
DROP INDEX "entityeventsoutbox_next_retry_at_correlation_id_created_at";
-- reverse: create index "entityeventsoutbox_correlation_id_created_at" to table: "event_outbox"
DROP INDEX "entityeventsoutbox_correlation_id_created_at";
-- reverse: modify "event_outbox" table
ALTER TABLE "event_outbox" ALTER COLUMN "retry_count" TYPE integer, ALTER COLUMN "created_at" SET DEFAULT now();
-- reverse: drop index "idx_event_outbox_user_created" from table: "event_outbox"
CREATE INDEX "idx_event_outbox_user_created" ON "event_outbox" ("user_id", "created_at");
-- reverse: drop index "idx_event_outbox_tenant" from table: "event_outbox"
CREATE INDEX "idx_event_outbox_tenant" ON "event_outbox" ("tenant_id", "created_at") WHERE ((published_at IS NULL) AND (dead_at IS NULL));
-- reverse: drop index "idx_event_outbox_polling_v2" from table: "event_outbox"
CREATE INDEX "idx_event_outbox_polling_v2" ON "event_outbox" ("next_retry_at" NULLS FIRST, "correlation_id", "created_at") WHERE ((published_at IS NULL) AND (dead_at IS NULL));
-- reverse: drop index "idx_event_outbox_correlation" from table: "event_outbox"
CREATE INDEX "idx_event_outbox_correlation" ON "event_outbox" ("correlation_id", "created_at") WHERE ((published_at IS NULL) AND (dead_at IS NULL));
