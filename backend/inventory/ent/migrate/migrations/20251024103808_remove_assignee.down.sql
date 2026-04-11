-- reverse: modify "collection_movements" table
ALTER TABLE "collection_movements" ADD COLUMN "assignment_date" timestamptz NULL, ADD COLUMN "assignee" character varying NULL;
