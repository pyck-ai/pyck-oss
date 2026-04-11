-- reverse: create "device_users" table
DROP TABLE "device_users";
-- reverse: create "device_locations" table
DROP TABLE "device_locations";
-- reverse: create index "location_tenant_id_name" to table: "locations"
DROP INDEX "location_tenant_id_name";
-- reverse: create "locations" table
DROP TABLE "locations";
-- reverse: create index "device_tenant_id_name" to table: "devices"
DROP INDEX "device_tenant_id_name";
-- reverse: create "devices" table
DROP TABLE "devices";
