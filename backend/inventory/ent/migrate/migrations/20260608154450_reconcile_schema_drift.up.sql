-- drop index "idx_event_outbox_polling_v2" from table: "event_outbox"
DROP INDEX "idx_event_outbox_polling_v2";
-- drop index "idx_event_outbox_transaction" from table: "event_outbox"
DROP INDEX "idx_event_outbox_transaction";
-- create index "entityeventsoutbox_next_retry_at_transaction_id_created_at" to table: "event_outbox"
CREATE INDEX "entityeventsoutbox_next_retry_at_transaction_id_created_at" ON "event_outbox" ("next_retry_at", "transaction_id", "created_at") WHERE ((published_at IS NULL) AND (dead_at IS NULL));
-- create index "entityeventsoutbox_transaction_id_created_at" to table: "event_outbox"
CREATE INDEX "entityeventsoutbox_transaction_id_created_at" ON "event_outbox" ("transaction_id", "created_at") WHERE ((published_at IS NULL) AND (dead_at IS NULL));
-- set comment to column: "transaction_id" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."transaction_id" IS '';
-- set comment to column: "trace_id" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."trace_id" IS '';
-- set comment to column: "request_id" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."request_id" IS '';
