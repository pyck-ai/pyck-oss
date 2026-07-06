-- drop "user_roles" table
DROP TABLE "user_roles";
-- drop "group_users" table
DROP TABLE "group_users";
-- drop "group_roles" table
DROP TABLE "group_roles";
-- drop "policies" table
DROP TABLE "policies";
-- drop "roles" table
DROP TABLE "roles";
-- drop "groups" table
DROP TABLE "groups";
-- modify "users" table
ALTER TABLE "users" DROP COLUMN "roles";
