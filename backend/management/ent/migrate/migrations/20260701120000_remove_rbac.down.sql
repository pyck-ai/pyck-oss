-- reverse: modify "users" table
ALTER TABLE "users" ADD COLUMN "roles" jsonb NOT NULL DEFAULT '[]'::jsonb;
-- reverse: drop "groups" table
CREATE TABLE "groups" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "created_by" uuid NOT NULL, "updated_at" timestamptz NULL, "updated_by" uuid NULL, "deleted_at" timestamptz NULL, "deleted_by" uuid NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "description" character varying NULL, PRIMARY KEY ("id"));
CREATE UNIQUE INDEX "group_name_tenant_id" ON "groups" ("name", "tenant_id") WHERE (deleted_at IS NULL);
-- reverse: drop "roles" table
CREATE TABLE "roles" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "created_by" uuid NOT NULL, "updated_at" timestamptz NULL, "updated_by" uuid NULL, "deleted_at" timestamptz NULL, "deleted_by" uuid NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "description" character varying NULL, PRIMARY KEY ("id"));
CREATE UNIQUE INDEX "role_name_tenant_id" ON "roles" ("name", "tenant_id") WHERE (deleted_at IS NULL);
-- reverse: drop "policies" table
CREATE TABLE "policies" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "created_by" uuid NOT NULL, "updated_at" timestamptz NULL, "updated_by" uuid NULL, "deleted_at" timestamptz NULL, "deleted_by" uuid NULL, "tenant_id" uuid NOT NULL, "resource" character varying NOT NULL, "action" character varying NOT NULL, "effect" character varying NOT NULL DEFAULT 'allow', "role_policies" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "policies_roles_policies" FOREIGN KEY ("role_policies") REFERENCES "roles" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
CREATE INDEX "accesspolicy_resource_action_tenant_id_deleted_at" ON "policies" ("resource", "action", "tenant_id", "deleted_at");
CREATE INDEX "accesspolicy_tenant_id_deleted_at" ON "policies" ("tenant_id", "deleted_at");
-- reverse: drop "group_roles" table
CREATE TABLE "group_roles" ("group_id" uuid NOT NULL, "role_id" uuid NOT NULL, PRIMARY KEY ("group_id", "role_id"), CONSTRAINT "group_roles_group_id" FOREIGN KEY ("group_id") REFERENCES "groups" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "group_roles_role_id" FOREIGN KEY ("role_id") REFERENCES "roles" ("id") ON UPDATE NO ACTION ON DELETE CASCADE);
-- reverse: drop "group_users" table
CREATE TABLE "group_users" ("group_id" uuid NOT NULL, "user_id" uuid NOT NULL, PRIMARY KEY ("group_id", "user_id"), CONSTRAINT "group_users_group_id" FOREIGN KEY ("group_id") REFERENCES "groups" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "group_users_user_id" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE);
-- reverse: drop "user_roles" table
CREATE TABLE "user_roles" ("user_id" uuid NOT NULL, "role_id" uuid NOT NULL, PRIMARY KEY ("user_id", "role_id"), CONSTRAINT "user_roles_role_id" FOREIGN KEY ("role_id") REFERENCES "roles" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "user_roles_user_id" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE);
