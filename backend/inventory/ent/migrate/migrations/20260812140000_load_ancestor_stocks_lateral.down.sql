-- Revert to the DISTINCT ON form installed by 20260812120000.
CREATE OR REPLACE FUNCTION inventory.load_ancestor_stocks(
    p_tenant_id       uuid,
    p_seeds           jsonb,
    p_items           jsonb,
    p_include_deleted boolean
) RETURNS TABLE (
    repository_id      uuid,
    parent_id          uuid,
    virtual_repo       boolean,
    item_id            uuid,
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
