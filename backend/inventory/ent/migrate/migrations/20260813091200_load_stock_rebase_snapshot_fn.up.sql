-- load_stock_rebase_snapshot — per (repo, item) version floor and live base for
-- the stock fan-out in CreateRepositoryMovement / DeleteRepositoryMovement.
--
--   floor: max(version) over ALL rows, soft-deleted included, so the version the
--          caller assigns clears the whole unique index.
--   live:  highest-version non-deleted row (deleted_at IS NULL or zero-time,
--          mirroring HistoryMixin), base for written = live + (walked - old).
--
-- One SELECT, so floor and live share a snapshot: a concurrent row already
-- committed is absorbed into the base, one committed after takes the version we
-- assign and the insert collides (23505 -> errOCCConflict -> gqltx retry).
-- STABLE keeps that guarantee; a VOLATILE body would re-snapshot per sub-query.
--
-- Both reads are served by stock_tenant_id_repository_id_item_id_version, and
-- pairs arrive as an exact [repo, item] jsonb list rather than a repo × item
-- cross product, so the function never walks a pair's history.

CREATE OR REPLACE FUNCTION inventory.load_stock_rebase_snapshot(
    p_tenant_id uuid,
    p_pairs     jsonb   -- array of [repository_id, item_id] uuid-string pairs
) RETURNS TABLE (
    repository_id           uuid,
    item_id                 uuid,
    floor_version           bigint,   -- NULL when the pair has no rows
    has_live                boolean,
    live_version            bigint,
    live_quantity           bigint,
    live_incoming_stock     bigint,
    live_outgoing_stock     bigint,
    live_own_quantity       bigint,
    live_own_incoming_stock bigint,
    live_own_outgoing_stock bigint
)
LANGUAGE sql
STABLE
AS $fn$
    SELECT
        k.rid AS repository_id,
        k.iid AS item_id,
        (
            SELECT max(s.version)
            FROM   inventory.stocks s
            WHERE  s.tenant_id     = p_tenant_id
              AND  s.repository_id = k.rid
              AND  s.item_id       = k.iid
        ) AS floor_version,
        COALESCE(live.found, false)          AS has_live,
        COALESCE(live.version, 0)            AS live_version,
        COALESCE(live.quantity, 0)           AS live_quantity,
        COALESCE(live.incoming_stock, 0)     AS live_incoming_stock,
        COALESCE(live.outgoing_stock, 0)     AS live_outgoing_stock,
        COALESCE(live.own_quantity, 0)       AS live_own_quantity,
        COALESCE(live.own_incoming_stock, 0) AS live_own_incoming_stock,
        COALESCE(live.own_outgoing_stock, 0) AS live_own_outgoing_stock
    FROM (
        SELECT (elem->>0)::uuid AS rid,
               (elem->>1)::uuid AS iid
        FROM   jsonb_array_elements(p_pairs) AS elem
    ) AS k
    LEFT JOIN LATERAL (
        SELECT true AS found,
               s.version, s.quantity, s.incoming_stock, s.outgoing_stock,
               s.own_quantity, s.own_incoming_stock, s.own_outgoing_stock
        FROM   inventory.stocks s
        WHERE  s.tenant_id     = p_tenant_id
          AND  s.repository_id = k.rid
          AND  s.item_id       = k.iid
          AND  (s.deleted_at IS NULL
                OR s.deleted_at = TIMESTAMPTZ '0001-01-01 00:00:00+00')
        ORDER  BY s.version DESC
        LIMIT  1
    ) AS live ON TRUE;
$fn$;

COMMENT ON FUNCTION inventory.load_stock_rebase_snapshot(uuid, jsonb) IS
'Per-(repo,item) version floor (max over all rows incl. soft-deleted) and live '
'base (highest-version non-deleted row) for the repository-movement stock '
'rebase. Called by service.stock.(*service).loadStockRebaseSnapshot.';
