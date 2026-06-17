-- create_item_movement_proc — server-side equivalent of
-- service.stock.(*service).CreateItemMovement.
--
-- Why this exists: each invocation of the resolver issues N round trips
-- (one per stock-row insert) plus the movement INSERT and the
-- ancestor-stock load. The bench *proc variants (dlabproc,
-- laltcor2b8proc) demonstrate a material throughput uplift when the
-- entire create flow lives in PL/pgSQL with a server-side retry LOOP:
-- one round trip per mutation instead of N, and OCC retries cost only
-- a per-iteration BEGIN/EXCEPTION instead of a fresh client tx.
-- See FINDINGS section 2C ("server-side retry") and section 4 step 8
-- (stored procedure for createItemMovement).
--
-- Functional equivalence with the Go path:
--
--   1. Look up FROM and TO repositories. Reject if both virtual.
--   2. For non-virtual FROM, verify available stock (latest snapshot
--      per (repo, item)) is >= requested quantity.
--   3. Walk parent_id ancestors of FROM and TO (recursive CTE) to the
--      tenant tree root, capped at ancestorWalkDepthCap (=64 to match
--      backend/inventory/service/stock/ancestor_loader.go).
--   4. Compute the LCA of FROM and TO from the loaded ancestor set.
--      Above the LCA the +q and -q deltas cancel; the walk stops
--      there. When there is no LCA (uuid.Nil branch in Go), each side
--      walks to its own root.
--   5. Apply the FROM-walk (-q) and the TO-walk (+q) deltas onto the
--      latest stock snapshot per ancestor. own_* fields are tracked
--      only on the direct FROM/TO endpoint. Virtual repos clamp
--      Quantity / OwnQuantity to 0 (FINDINGS constraint #3).
--   6. INSERT the movement row.
--   7. INSERT the affected stock rows in a single batch with
--      version = max(version) + 1 per (tenant, repo, item) group,
--      matching the OCC contract introduced in Phase 6.1.
--
-- Retry shape: the body is wrapped in LOOP { BEGIN ... EXCEPTION WHEN
-- unique_violation } with a budget of 50 (matches PYCK_TX_RETRIES /
-- gqltx default). On the unique-index 23505 against
-- stock_tenant_id_repository_id_item_id_version we retry, recomputing
-- the version from the current max — that is the OCC retry point. Any
-- other constraint violation surfaces verbatim. After the budget is
-- exhausted we re-RAISE so the caller (gqltx) can either retry the
-- whole tx or surface the conflict.

CREATE OR REPLACE FUNCTION inventory.create_item_movement_proc(
    p_tenant_id      uuid,
    p_item_id        uuid,
    p_from_id        uuid,
    p_to_id          uuid,
    p_quantity       bigint,
    p_handler        text,
    p_collection_id  uuid,
    p_order_id       uuid,
    p_position       int,
    p_data_type_id   uuid,
    p_data_type_slug text,
    p_data           jsonb,
    p_created_by     uuid,
    p_movement_id    uuid
) RETURNS uuid
LANGUAGE plpgsql
AS $proc$
DECLARE
    v_from_virtual  boolean;
    v_to_virtual    boolean;
    v_available     bigint;
    v_lca_id        uuid;
    v_retries       int := 0;
    v_max_retries   constant int := 50;
    v_movement_id   uuid := COALESCE(p_movement_id, gen_random_uuid());
    v_now           timestamptz := now();
BEGIN
    -- ── 1. Resolve FROM / TO virtual flags ─────────────────────────
    SELECT virtual_repo INTO v_from_virtual
    FROM   repositories
    WHERE  tenant_id = p_tenant_id AND id = p_from_id AND deleted_at IS NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'REPO_NOT_FOUND: from %', p_from_id;
    END IF;

    SELECT virtual_repo INTO v_to_virtual
    FROM   repositories
    WHERE  tenant_id = p_tenant_id AND id = p_to_id AND deleted_at IS NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'REPO_NOT_FOUND: to %', p_to_id;
    END IF;

    IF v_from_virtual AND v_to_virtual THEN
        RAISE EXCEPTION 'movements between virtual repositories are not allowed';
    END IF;

    -- ── 2. Stock-availability check on non-virtual FROM ────────────
    -- Reads the latest stock snapshot for (FROM, item); a missing row
    -- means zero. Mirrors the ent.Desc(created_at).First(ctx) lookup
    -- in CreateItemMovement.
    IF NOT v_from_virtual THEN
        SELECT COALESCE(s.quantity, 0)
             + COALESCE(s.incoming_stock, 0)
             - COALESCE(s.outgoing_stock, 0)
        INTO v_available
        FROM (
            SELECT quantity, incoming_stock, outgoing_stock
            FROM   stocks
            WHERE  tenant_id = p_tenant_id
              AND  repository_id = p_from_id
              AND  item_id = p_item_id
              AND  deleted_at IS NULL
            ORDER  BY created_at DESC, version DESC
            LIMIT  1
        ) s;
        IF COALESCE(v_available, 0) < p_quantity THEN
            RAISE EXCEPTION 'STOCK_INSUFFICIENT: from=% item=% available=% requested=%',
                p_from_id, p_item_id, COALESCE(v_available, 0), p_quantity;
        END IF;
    END IF;

    -- ── 3. Build the ancestor closure for FROM and TO ──────────────
    -- The CTE matches loadAncestorIDs (ancestor_loader.go): seed with
    -- {FROM, TO}, walk parent_id, cap at depth 64, exclude soft-deleted.
    -- We also assign each ancestor a "side" mask so we can compute the
    -- LCA: the lowest-depth repo present in BOTH walks.
    DROP TABLE IF EXISTS pg_temp.tmp_ancestors;
    CREATE TEMP TABLE pg_temp.tmp_ancestors ON COMMIT DROP AS
    WITH RECURSIVE walk(id, parent_id, depth, side) AS (
        SELECT id, parent_id, 0, 1   -- 1 = FROM walk
        FROM   repositories
        WHERE  tenant_id = p_tenant_id AND id = p_from_id AND deleted_at IS NULL
        UNION ALL
        SELECT id, parent_id, 0, 2   -- 2 = TO walk
        FROM   repositories
        WHERE  tenant_id = p_tenant_id AND id = p_to_id AND deleted_at IS NULL
        UNION ALL
        SELECT r.id, r.parent_id, w.depth + 1, w.side
        FROM   repositories r
        JOIN   walk w ON r.id = w.parent_id
        WHERE  r.tenant_id = p_tenant_id AND r.deleted_at IS NULL AND w.depth < 64
    )
    SELECT id,
           BOOL_OR(side = 1) AS in_from,
           BOOL_OR(side = 2) AS in_to,
           MIN(CASE WHEN side = 1 THEN depth END) AS from_depth,
           MIN(CASE WHEN side = 2 THEN depth END) AS to_depth
    FROM walk
    GROUP BY id;

    -- LCA = the ancestor present in BOTH walks with the smallest
    -- combined depth. NULL when there is no shared ancestor (matches
    -- uuid.Nil behavior in lowestCommonAncestor).
    SELECT id INTO v_lca_id
    FROM   pg_temp.tmp_ancestors
    WHERE  in_from AND in_to
    ORDER  BY GREATEST(from_depth, to_depth) ASC, from_depth ASC
    LIMIT  1;

    -- Trim ancestors above the LCA: above it, the FROM-walk and
    -- TO-walk deltas cancel exactly, so there is nothing to write.
    -- When v_lca_id IS NULL we keep every node in either walk
    -- (historical "no LCA" behavior).
    IF v_lca_id IS NOT NULL THEN
        DELETE FROM pg_temp.tmp_ancestors
        WHERE (in_from AND from_depth > (SELECT from_depth FROM pg_temp.tmp_ancestors WHERE id = v_lca_id))
           OR (in_to   AND to_depth   > (SELECT to_depth   FROM pg_temp.tmp_ancestors WHERE id = v_lca_id))
           OR (NOT in_from AND NOT in_to);
    END IF;

    -- ── 4. Build the per-ancestor delta + latest-snapshot baseline ─
    -- One row per affected repository, holding:
    --   - the latest stock snapshot fields (or zeros if no row exists),
    --   - the latest version (-1 if no row exists; +1 = first version),
    --   - the net delta to apply (FROM-side: -q; TO-side: +q; both: 0).
    -- The delta rules mirror applyRepositoryStockDelta:
    --   * Quantity:        net delta, clamped to 0 for virtual repos.
    --   * IncomingStock:   only the TO-walk contributes (+q on entry,
    --                      consumed on the LCA cancellation).
    --   * OutgoingStock:   only the FROM-walk contributes.
    --   * Own*:            only on the direct FROM or TO endpoint
    --                      (ownStock=true at the leaf, false on the
    --                      ancestor recursion).
    DROP TABLE IF EXISTS pg_temp.tmp_stock_deltas;
    CREATE TEMP TABLE pg_temp.tmp_stock_deltas ON COMMIT DROP AS
    SELECT a.id AS repository_id,
           a.in_from,
           a.in_to,
           COALESCE(s.quantity, 0)           AS base_quantity,
           COALESCE(s.incoming_stock, 0)     AS base_incoming,
           COALESCE(s.outgoing_stock, 0)     AS base_outgoing,
           COALESCE(s.own_quantity, 0)       AS base_own_quantity,
           COALESCE(s.own_incoming_stock, 0) AS base_own_incoming,
           COALESCE(s.own_outgoing_stock, 0) AS base_own_outgoing,
           COALESCE(s.version, -1)           AS base_version,
           r.virtual_repo                    AS is_virtual,
           (a.id = p_from_id)                AS is_from_leaf,
           (a.id = p_to_id)                  AS is_to_leaf
    FROM   pg_temp.tmp_ancestors a
    JOIN   repositories r
      ON   r.tenant_id = p_tenant_id AND r.id = a.id
    LEFT JOIN LATERAL (
        SELECT s2.quantity, s2.incoming_stock, s2.outgoing_stock,
               s2.own_quantity, s2.own_incoming_stock, s2.own_outgoing_stock,
               s2.version
        FROM   stocks s2
        WHERE  s2.tenant_id = p_tenant_id
          AND  s2.repository_id = a.id
          AND  s2.item_id = p_item_id
          AND  s2.deleted_at IS NULL
        ORDER  BY s2.version DESC
        LIMIT  1
    ) s ON TRUE;

    -- ── 5. Retry loop — INSERT movement + stock rows ───────────────
    -- The body wraps movement INSERT + stock CreateBulk in a savepoint
    -- so a unique_violation on stock.version can be retried with a
    -- recomputed max(version) without losing the surrounding
    -- transaction (which also carries gqltx side effects). Mirrors the
    -- bench *proc retry pattern but matched to OCC instead of advisory
    -- locks.
    LOOP
        BEGIN
            -- Movement first — its primary key is gen_random_uuid by
            -- default so a duplicate is effectively impossible. If
            -- something does collide here, RAISE: a duplicate movement
            -- ID is a programmer error, not OCC contention.
            INSERT INTO item_movements (
                id, tenant_id,
                created_at, created_by,
                data_type_id, data_type_slug, data,
                item_id, from_id, to_id,
                quantity, executed,
                handler, collection_id, order_id, "position"
            )
            VALUES (
                v_movement_id, p_tenant_id,
                v_now, p_created_by,
                p_data_type_id, p_data_type_slug, COALESCE(p_data, '{}'::jsonb),
                p_item_id, p_from_id, p_to_id,
                p_quantity, FALSE,
                p_handler, p_collection_id, p_order_id, COALESCE(p_position, 0)
            );

            -- Stock rows. version = max(version)+1 per group, sourced
            -- from the snapshot we loaded plus a per-repo CASE that
            -- folds in the delta with the same clamp/own rules as
            -- applyRepositoryStockDelta.
            INSERT INTO stocks (
                id, tenant_id, created_at, created_by,
                repository_id, item_id, movement_id,
                quantity, incoming_stock, outgoing_stock,
                own_quantity, own_incoming_stock, own_outgoing_stock,
                version
            )
            SELECT
                gen_random_uuid(),
                p_tenant_id,
                v_now,
                p_created_by,
                d.repository_id,
                p_item_id,
                v_movement_id,
                -- Quantity: at CREATE time the baseline is preserved.
                -- The actual delta is applied later by ExecuteItemMovement
                -- via applyItemMovementStockDelta. Mirrors the Go path:
                -- simulateRepositoryStockMapWalk never assigns Quantity,
                -- it only bumps the pending Incoming/Outgoing counters.
                -- Virtual repos are still clamped to 0 (FINDINGS #3).
                CASE
                    WHEN d.is_virtual THEN 0
                    ELSE d.base_quantity
                END,
                -- IncomingStock: only the TO-walk pushes +q; the LCA
                -- cancellation is handled implicitly because the LCA
                -- is in both walks and their deltas net to zero on
                -- non-leaf nodes. On the LCA itself we keep the base.
                CASE
                    WHEN d.in_to AND NOT d.in_from THEN GREATEST(d.base_incoming + p_quantity, 0)
                    ELSE d.base_incoming
                END,
                CASE
                    WHEN d.in_from AND NOT d.in_to THEN GREATEST(d.base_outgoing + p_quantity, 0)
                    ELSE d.base_outgoing
                END,
                -- own_quantity: at CREATE time the baseline is preserved.
                -- The actual delta on the FROM / TO leaf is applied later
                -- by ExecuteItemMovement via applyItemMovementStockDelta.
                -- Mirrors the Go path: simulateRepositoryStockMapWalk
                -- never assigns OwnQuantity at create-time, only the
                -- OwnIncoming / OwnOutgoing reservations below.
                -- Virtual leaves are still clamped to 0.
                CASE
                    WHEN d.is_virtual AND (d.is_from_leaf OR d.is_to_leaf) THEN 0
                    ELSE d.base_own_quantity
                END,
                CASE
                    WHEN d.is_to_leaf THEN GREATEST(d.base_own_incoming + p_quantity, 0)
                    ELSE d.base_own_incoming
                END,
                CASE
                    WHEN d.is_from_leaf THEN GREATEST(d.base_own_outgoing + p_quantity, 0)
                    ELSE d.base_own_outgoing
                END,
                -- version = current_max + 1 per (tenant, repo, item).
                -- A unique_violation here is the OCC retry point.
                COALESCE(
                    (SELECT MAX(version)
                     FROM   stocks s3
                     WHERE  s3.tenant_id = p_tenant_id
                       AND  s3.repository_id = d.repository_id
                       AND  s3.item_id = p_item_id),
                    -1
                ) + 1
            FROM pg_temp.tmp_stock_deltas d
            -- Skip rows where the snapshot is unchanged — matches the
            -- skip-noop branch in insertStockMapWithVersions.
            WHERE NOT (
                d.in_from = FALSE AND d.in_to = FALSE
            );

            RETURN v_movement_id;

        EXCEPTION
            WHEN unique_violation THEN
                -- Only retry on the stock OCC index; surface anything
                -- else (e.g., movement PK collision, FK violations).
                IF SQLERRM NOT ILIKE '%stock_tenant_id_repository_id_item_id_version%' THEN
                    RAISE;
                END IF;
                v_retries := v_retries + 1;
                IF v_retries > v_max_retries THEN
                    RAISE;
                END IF;
                -- jittered backoff — same shape as the Go retry middleware
                PERFORM pg_sleep(0.001 * (1 + floor(random() * LEAST(v_retries, 10))));
        END;
    END LOOP;
END;
$proc$;

COMMENT ON FUNCTION inventory.create_item_movement_proc(
    uuid, uuid, uuid, uuid, bigint, text, uuid, uuid, int, uuid, text, jsonb, uuid, uuid
) IS
'Server-side equivalent of service.stock.(*service).CreateItemMovement. '
'Inserts one item_movement and the affected stock rows (FROM, TO, '
'ancestors up to LCA) in a single round trip with internal OCC retries '
'against stock_tenant_id_repository_id_item_id_version. Returns the '
'movement id. Step 7.1 / FINDINGS section 2C.';
