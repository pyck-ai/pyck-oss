-- drop indexes
DROP INDEX IF EXISTS "receiving"."idx_inbound_shipment_notifications_inbound_id";
DROP INDEX IF EXISTS "receiving"."idx_inbound_shipment_notifications_tenant_id";
-- drop "inbound-shipment-notifications" table
DROP TABLE IF EXISTS "receiving"."inbound-shipment-notifications";
