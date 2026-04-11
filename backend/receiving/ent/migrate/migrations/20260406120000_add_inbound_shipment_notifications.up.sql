-- create "inbound-shipment-notifications" table
CREATE TABLE "receiving"."inbound-shipment-notifications" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "created_by" uuid NULL, "updated_at" timestamptz NULL, "updated_by" uuid NULL, "deleted_at" timestamptz NULL, "deleted_by" uuid NULL, "data_type_id" uuid NULL, "data_type_slug" character varying NULL, "data" jsonb NULL, "inbound_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "inbound-shipment-notifications_inbounds_inboundShipmentNotifications" FOREIGN KEY ("inbound_id") REFERENCES "receiving"."inbounds" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- create indexes
CREATE INDEX "idx_inbound_shipment_notifications_tenant_id" ON "receiving"."inbound-shipment-notifications" ("tenant_id");
CREATE INDEX "idx_inbound_shipment_notifications_inbound_id" ON "receiving"."inbound-shipment-notifications" ("inbound_id");
