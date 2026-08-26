-- Serves the Relay walk over a parent's children, which sends no orderBy and
-- so paginates with ORDER BY id: only a composite index with id trailing
-- parent_id supplies both the filter and the ordering.
--
-- DROP first so hosts carrying the v0.23.1 hotfix, where this index is partial
-- on deleted_at IS NULL and therefore never usable, converge on this
-- definition. On hosts without the index the DROP is a no-op.
DROP INDEX IF EXISTS "repository_parent_id_id";
-- create index "repository_parent_id_id" to table: "repositories"
CREATE INDEX "repository_parent_id_id" ON "repositories" ("parent_id", "id");
