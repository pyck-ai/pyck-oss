-- load_ancestor_stocks — server-side, index-served ancestor closure + current
-- stock per (repo, item) for service.stock's repository-movement handlers
-- (loadAncestorStocks). Replaces the Go path's three round trips — the recursive
-- parent walk, the repository hydration, and a NOT EXISTS anti-join stock scan
-- that sequential-scanned and contended on the version unique index, deadlocking
-- under a bulk-assign burst — with a single call.
--
--   - ancestors: recursive parent_id walk from the seeds, tenant-scoped and
--     depth-capped at 100 (mirrors loadAncestorIDs). Returns id, parent_id,
--     virtual_repo — the only repository fields callers read.
--   - current stock per (repo, item): DISTINCT ON (repository_id, item_id)
--     ORDER BY version DESC, served by the (repository_id, item_id, version DESC)
--     index — no anti-join, no history walk. "Current" is the highest version,
--     not the latest created_at (created_at is pod wall clock, not a total order).
--
-- One SELECT LEFT JOINs the ancestors to their current stock, so a repo that
-- holds no stock still returns a row (stock columns NULL). Seeds and items are
-- passed as jsonb uuid-string arrays (as load_stock_rebase_snapshot does) so the
-- call needs no driver array support.
CREATE OR REPLACE FUNCTION inventory.load_ancestor_stocks(
    p_tenant_id       uuid,
    p_seeds           jsonb,   -- array of repository-id uuid strings
    p_items           jsonb,   -- array of item-id uuid strings (empty = no stock)
    p_include_deleted boolean
) RETURNS TABLE (
    repository_id      uuid,
    parent_id          uuid,   -- zero uuid for a tree root
    virtual_repo       boolean,
    item_id            uuid,   -- NULL when the repo holds no matching stock
    version            bigint,
    quantity           bigint,
    own_quantity       bigint,
    incoming_stock     bigint,
    outgoing_stock     bigint,
    own_incoming_stock bigint,
    own_outgoing_stock bigint
)
LANGUAGE sql
STABLE
AS $fn$
    WITH RECURSIVE ancestors(id, parent_id, virtual_repo, depth) AS (
        SELECT r.id, r.parent_id, r.virtual_repo, 0
        FROM   inventory.repositories r
        WHERE  r.tenant_id = p_tenant_id
          AND  r.id IN (SELECT e::uuid FROM jsonb_array_elements_text(p_seeds) AS e)
          AND  (p_include_deleted OR r.deleted_at IS NULL)
        UNION ALL
        SELECT r.id, r.parent_id, r.virtual_repo, a.depth + 1
        FROM   inventory.repositories r
        JOIN   ancestors a ON r.id = a.parent_id
        WHERE  r.tenant_id = p_tenant_id
          AND  a.depth < 100
          AND  (p_include_deleted OR r.deleted_at IS NULL)
    ),
    anc AS (
        SELECT DISTINCT id, parent_id, virtual_repo FROM ancestors
    ),
    latest AS (
        SELECT DISTINCT ON (s.repository_id, s.item_id)
               s.repository_id, s.item_id, s.version, s.quantity, s.own_quantity,
               s.incoming_stock, s.outgoing_stock, s.own_incoming_stock, s.own_outgoing_stock
        FROM   inventory.stocks s
        JOIN   anc ON anc.id = s.repository_id
        WHERE  s.tenant_id = p_tenant_id
          AND  s.item_id IN (SELECT e::uuid FROM jsonb_array_elements_text(p_items) AS e)
          AND  (p_include_deleted OR s.deleted_at IS NULL)
        ORDER  BY s.repository_id, s.item_id, s.version DESC
    )
    SELECT
        anc.id AS repository_id,
        COALESCE(anc.parent_id, '00000000-0000-0000-0000-000000000000'::uuid) AS parent_id,
        anc.virtual_repo,
        latest.item_id,
        latest.version, latest.quantity, latest.own_quantity,
        latest.incoming_stock, latest.outgoing_stock,
        latest.own_incoming_stock, latest.own_outgoing_stock
    FROM anc
    LEFT JOIN latest ON latest.repository_id = anc.id;
$fn$;

COMMENT ON FUNCTION inventory.load_ancestor_stocks(uuid, jsonb, jsonb, boolean) IS
'Index-served ancestor closure (recursive parent_id walk) plus the current '
'(highest-version) stock per (repo, item) for the repository-movement handlers. '
'Replaces the Go loadAncestorStocks three round trips and its NOT EXISTS '
'anti-join. Seeds and items passed as jsonb uuid-string arrays.';
