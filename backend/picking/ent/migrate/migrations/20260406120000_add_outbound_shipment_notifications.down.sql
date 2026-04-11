-- drop indexes
DROP INDEX IF EXISTS "picking"."idx_outbound_shipment_notifications_order_id";
DROP INDEX IF EXISTS "picking"."idx_outbound_shipment_notifications_tenant_id";
-- drop "outbound-shipment-notifications" table
DROP TABLE IF EXISTS "picking"."outbound-shipment-notifications";
