-- load_ancestor_stocks — replace the DISTINCT ON stock scan installed by
-- 20260812120000 with a per-(repo, item) LATERAL top-1, the same index-served
-- shape as load_stock_rebase_snapshot.
--
-- The DISTINCT ON (repository_id, item_id) ORDER BY version DESC form read and
-- sorted EVERY stock version for every (ancestor repo, item) pair before
-- reducing to the current row — on a hot shared ancestor that is tens of
-- thousands of rows scanned to yield a handful. The rewrite builds the exact
-- (repo, item) pair set (anc × items) and does one backward index seek per pair
-- on (repository_id, item_id, version DESC) via JOIN LATERAL ... LIMIT 1, so the
-- stock-side row count drops from O(all versions in the closure) to O(pairs).
--
-- Semantics are unchanged from 20260812120000: "current" is the highest-version
-- non-deleted row per (repo, item); the final anc LEFT JOIN latest still returns
-- a row for a repo that holds no matching stock (stock columns NULL). Only the
-- stock CTE changed — the recursive ancestor walk is untouched.
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
    -- Exact (repo, item) pairs to look up: the ancestor closure × the moved
    -- items. Bounded, so the stock read below is one index seek per pair.
    pairs AS (
        SELECT anc.id AS repository_id, i.iid AS item_id
        FROM   anc
        CROSS  JOIN (SELECT e::uuid AS iid FROM jsonb_array_elements_text(p_items) AS e) i
    ),
    -- Current (highest-version, non-deleted) stock per pair, one backward
    -- index scan each. INNER JOIN LATERAL drops pairs that hold no row.
    latest AS (
        SELECT p.repository_id, p.item_id, live.version, live.quantity, live.own_quantity,
               live.incoming_stock, live.outgoing_stock, live.own_incoming_stock, live.own_outgoing_stock
        FROM   pairs p
        JOIN   LATERAL (
            SELECT s.version, s.quantity, s.own_quantity,
                   s.incoming_stock, s.outgoing_stock, s.own_incoming_stock, s.own_outgoing_stock
            FROM   inventory.stocks s
            WHERE  s.tenant_id     = p_tenant_id
              AND  s.repository_id = p.repository_id
              AND  s.item_id       = p.item_id
              AND  (p_include_deleted OR s.deleted_at IS NULL)
            ORDER  BY s.version DESC
            LIMIT  1
        ) live ON TRUE
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
'(highest-version) stock per (repo, item) via a per-pair JOIN LATERAL ... '
'ORDER BY version DESC LIMIT 1 (same shape as load_stock_rebase_snapshot). '
'Seeds and items passed as jsonb uuid-string arrays.';
