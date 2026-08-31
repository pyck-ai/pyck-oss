-- #990 precondition guard — runs BEFORE the datatype version migration.
--
-- B5/B8 removed the legacy data_type_slug -> DataType fallback: a DataMixin row
-- is now resolved only via its pinned data_type_id. Any pre-existing row that
-- has a data_type_slug but a NULL data_type_id can no longer be resolved (writes
-- to it fail with ErrDataTypeNotSet, AnalyzeImageFile errors, etc.). There is no
-- automated backfill -- slug->id resolution crosses service boundaries -- so
-- rather than silently serving runtime errors, we refuse to migrate/boot until
-- such rows are backfilled by hand. The invariant is expected to already hold;
-- if it ever fails, the data is corrected manually before the deploy proceeds.
--
-- The check discovers every DataMixin table dynamically -- any BASE TABLE
-- carrying BOTH data_type_slug and data_type_id columns -- across all pyck
-- schemas (they share one database), so new DataMixin entities are covered
-- automatically and no hard-coded table list can drift out of date. Views are
-- excluded (information_schema.columns lists them too, which would double-
-- count), and a table carrying only one of the two columns is skipped rather
-- than crashing the generated per-table query with a Postgres internal error.
-- On a fresh deploy some service schemas may not exist yet; that is fine (no
-- data to guard). On an existing deploy every schema is present before this
-- runs, so the check is complete.
--
-- Soft-deleted rows are counted deliberately: re-registering a tenant upserts
-- onto the tenant's existing row and clears deleted_at, so a soft-deleted row
-- can come back live -- and would come back unresolvable. Every row that can
-- be resurrected has to carry a pinned data_type_id, so the count carries no
-- deleted_at predicate.
--
-- OPERATOR NOTES (this guard exists specifically to be hit by a human):
--   * Recovery: migrations run through golang-migrate, so this RAISE marks
--     schema_migrations.dirty = true and every subsequent boot fails with
--     "Dirty database version 20260720120000" -- even after the rows have
--     been backfilled. After fixing the data, ALSO reset the dirty flag to
--     the PREVIOUS migration (versions are timestamps, not sequential):
--       migrate force 20260716120000
--     (equivalently: UPDATE <service>.schema_migrations
--        SET version = 20260716120000, dirty = false;)
--     Do NOT force 20260720120000 itself -- that marks this guard as applied
--     and skips it.
--   * Deploy window: this check runs once, when management's migrations
--     apply, and never again. The other services deploy independently, so
--     until each has cycled onto post-#990 images its old replicas keep
--     serving the pre-#990 GraphQL surface -- including clearDataTypeID on
--     every DataMixin update input, which mints exactly the NULL
--     data_type_id + non-empty data_type_slug shape this check refuses, and
--     in-place json_schema edits, which break the append-only DataType
--     invariant. Roll every service promptly once this migration has
--     applied, and do not roll an individual service back to pre-#990
--     images afterwards: rows minted in that window go uncaught, because
--     the migration is already recorded as applied.
--   * Permissions: the loop SELECTs from every pyck service schema
--     (inventory, picking, receiving, workflow, ...), so the role running
--     management's migrations needs cross-schema SELECT; otherwise the guard
--     fails with "permission denied" instead of the precondition message.
DO $$
DECLARE
    r      RECORD;
    bad    bigint;
    total  bigint := 0;
    detail text   := '';
BEGIN
    FOR r IN
        SELECT c.table_schema, c.table_name
        FROM   information_schema.columns c
        JOIN   information_schema.tables t
          ON   t.table_schema = c.table_schema
         AND   t.table_name   = c.table_name
         AND   t.table_type   = 'BASE TABLE'
        WHERE  c.column_name = 'data_type_slug'
          AND  c.table_schema NOT IN ('pg_catalog', 'information_schema')
          AND  EXISTS (
                 SELECT 1 FROM information_schema.columns c2
                 WHERE  c2.table_schema = c.table_schema
                   AND  c2.table_name   = c.table_name
                   AND  c2.column_name  = 'data_type_id'
               )
    LOOP
        EXECUTE format(
            'SELECT count(*) FROM %I.%I WHERE data_type_id IS NULL AND coalesce(data_type_slug, '''') <> ''''',
            r.table_schema, r.table_name
        ) INTO bad;

        IF bad > 0 THEN
            total  := total + bad;
            detail := detail || format('  %s.%s: %s row(s)', r.table_schema, r.table_name, bad) || chr(10);
        END IF;
    END LOOP;

    IF total > 0 THEN
        RAISE EXCEPTION
            'precondition failed (#990): % DataMixin row(s) have data_type_slug but NULL data_type_id; backfill data_type_id before deploying:%',
            total, chr(10) || detail;
    END IF;
END $$;
