-- reverse: create "user_roles" table
DROP TABLE "user_roles";
-- reverse: create index "accesspolicy_tenant_id_deleted_at" to table: "policies"
DROP INDEX "accesspolicy_tenant_id_deleted_at";
-- reverse: create index "accesspolicy_resource_action_tenant_id_deleted_at" to table: "policies"
DROP INDEX "accesspolicy_resource_action_tenant_id_deleted_at";
-- reverse: create "policies" table
DROP TABLE "policies";
-- reverse: create "group_users" table
DROP TABLE "group_users";
-- reverse: create "group_roles" table
DROP TABLE "group_roles";
-- reverse: create index "role_name_tenant_id" to table: "roles"
DROP INDEX "role_name_tenant_id";
-- reverse: create "roles" table
DROP TABLE "roles";
-- reverse: create index "group_name_tenant_id" to table: "groups"
DROP INDEX "group_name_tenant_id";
-- reverse: create "groups" table
DROP TABLE "groups";