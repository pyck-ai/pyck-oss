-- create "outbound-shipment-notifications" table
CREATE TABLE "picking"."outbound-shipment-notifications" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "created_by" uuid NULL, "updated_at" timestamptz NULL, "updated_by" uuid NULL, "deleted_at" timestamptz NULL, "deleted_by" uuid NULL, "data_type_id" uuid NULL, "data_type_slug" character varying NULL, "data" jsonb NULL, "order_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "outbound-shipment-notifications_orders_outboundShipmentNotifications" FOREIGN KEY ("order_id") REFERENCES "picking"."orders" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- create indexes
CREATE INDEX "idx_outbound_shipment_notifications_tenant_id" ON "picking"."outbound-shipment-notifications" ("tenant_id");
CREATE INDEX "idx_outbound_shipment_notifications_order_id" ON "picking"."outbound-shipment-notifications" ("order_id");
