-- drop index "idx_event_outbox_polling_v2" from table: "event_outbox"
DROP INDEX "idx_event_outbox_polling_v2";
-- drop index "idx_event_outbox_tenant" from table: "event_outbox"
DROP INDEX "idx_event_outbox_tenant";
-- drop index "idx_event_outbox_transaction" from table: "event_outbox"
DROP INDEX "idx_event_outbox_transaction";
-- drop index "idx_event_outbox_user_created" from table: "event_outbox"
DROP INDEX "idx_event_outbox_user_created";
-- modify "event_outbox" table
ALTER TABLE "event_outbox" ALTER COLUMN "created_at" DROP DEFAULT, ALTER COLUMN "retry_count" TYPE bigint;
-- create index "entityeventsoutbox_next_retry_at_transaction_id_created_at" to table: "event_outbox"
CREATE INDEX "entityeventsoutbox_next_retry_at_transaction_id_created_at" ON "event_outbox" ("next_retry_at", "transaction_id", "created_at") WHERE ((published_at IS NULL) AND (dead_at IS NULL));
-- create index "entityeventsoutbox_tenant_id_created_at" to table: "event_outbox"
CREATE INDEX "entityeventsoutbox_tenant_id_created_at" ON "event_outbox" ("tenant_id", "created_at") WHERE ((published_at IS NULL) AND (dead_at IS NULL));
-- create index "entityeventsoutbox_transaction_id_created_at" to table: "event_outbox"
CREATE INDEX "entityeventsoutbox_transaction_id_created_at" ON "event_outbox" ("transaction_id", "created_at") WHERE ((published_at IS NULL) AND (dead_at IS NULL));
-- create index "entityeventsoutbox_user_id_created_at" to table: "event_outbox"
CREATE INDEX "entityeventsoutbox_user_id_created_at" ON "event_outbox" ("user_id", "created_at");
-- set comment to table: "event_outbox"
COMMENT ON TABLE "event_outbox" IS '';
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
-- set comment to column: "transaction_id" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."transaction_id" IS '';
-- set comment to column: "trace_id" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."trace_id" IS '';
-- set comment to column: "request_id" on table: "event_outbox"
COMMENT ON COLUMN "event_outbox"."request_id" IS '';
-- drop index "idx_inbound_shipment_notifications_inbound_id" from table: "inbound-shipment-notifications"
DROP INDEX "idx_inbound_shipment_notifications_inbound_id";
-- drop index "idx_inbound_shipment_notifications_tenant_id" from table: "inbound-shipment-notifications"
DROP INDEX "idx_inbound_shipment_notifications_tenant_id";
-- modify "inbound-shipment-notifications" table
ALTER TABLE "inbound-shipment-notifications" DROP CONSTRAINT "inbound-shipment-notifications_inbounds_inboundShipmentNotifica", ALTER COLUMN "created_by" SET NOT NULL, ADD CONSTRAINT "inbound-shipment-notifications_e5881d9bdeae59a11fde42034a069772" FOREIGN KEY ("inbound_id") REFERENCES "inbounds" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION;
-- create index "inboundshipmentnotification_inbound_id" to table: "inbound-shipment-notifications"
CREATE INDEX "inboundshipmentnotification_inbound_id" ON "inbound-shipment-notifications" ("inbound_id");
-- create index "inboundshipmentnotification_tenant_id" to table: "inbound-shipment-notifications"
CREATE INDEX "inboundshipmentnotification_tenant_id" ON "inbound-shipment-notifications" ("tenant_id");
