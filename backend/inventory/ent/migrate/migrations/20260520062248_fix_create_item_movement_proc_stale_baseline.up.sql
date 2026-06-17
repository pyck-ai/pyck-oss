-- Fix stale-baseline race in inventory.create_item_movement_proc.
--
-- The previous body (migration 20260430070249) built a temporary table
-- pg_temp.tmp_stock_deltas ONCE before its OCC retry LOOP, then INSERTed
-- into stocks inside the LOOP, recomputing only the `version` column
-- inline from MAX(version)+1. On a unique_violation retry the INSERT
-- picked a fresh version but reused the stale base_quantity /
-- base_own_quantity from tmp_stock_deltas — which is never refreshed.
-- When a concurrent transaction committed a new stocks row between the
-- proc's tmp_stock_deltas snapshot and the proc's first INSERT attempt,
-- the resulting retry would land at a fresh version with the
-- pre-concurrent-commit baseline, poisoning the "latest stocks by version
-- DESC" projection. See
-- issue-stock-map-resets-own-quantity-to-zero-on-pending-pick-creation.md
-- and stocks_race_proc_test.go for the regression that pins this surface.
--
-- Fix: collapse the per-ancestor delta build into an inline subselect
-- in the stocks INSERT statement itself. The LATERAL subquery is now
-- re-evaluated for each INSERT attempt (because it is part of the
-- statement, not a precomputed temp table), so on a unique_violation
-- retry the EXCEPTION handler re-enters the BEGIN block and the next
-- INSERT observes the freshly-committed stocks state — including any
-- row that landed between the proc start and the retry. No temp table
-- needed; no DDL inside the retry loop.
--
-- All other behavior is verbatim from migration 20260430070249:
--   • FROM/TO virtual flag resolution                                (§1)
--   • Availability check on non-virtual FROM                         (§2)
--   • Ancestor closure recursive CTE + LCA trim                      (§3)
--   • Server-side OCC retry LOOP with 50-attempt budget and jittered
--     backoff matching the Go gqltx retry middleware                 (§4)
--
-- The proc's signature is unchanged so this is a true CREATE OR REPLACE.

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

    -- ── 4. Retry loop — INSERT movement + stock rows ───────────────
    -- The body wraps movement INSERT + stock INSERT in a savepoint so a
    -- unique_violation on stock.version can be retried with a recomputed
    -- max(version) without losing the surrounding transaction (which
    -- also carries gqltx side effects). Each retry re-evaluates the
    -- inline LATERAL baseline against the now-visible stocks state, so
    -- a concurrent committer's row is observed on the next attempt and
    -- the resulting INSERT carries a CONSISTENT (version, baseline) pair
    -- rather than a fresh version + stale baseline. Matches the Go path
    -- fix in insertStockMapWithVersions (impl.go:1610), which sources
    -- Quantity / OwnQuantity from a freshly re-read latest map instead
    -- of from the much-earlier loadAncestorStocks baseline.
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

            -- Stock rows. version = max(version)+1 per group, baseline
            -- sourced via an INLINE LATERAL pick of the latest stocks
            -- row per (repo, item). Crucially, the LATERAL re-evaluates
            -- on each INSERT attempt — the EXCEPTION/retry path rolls
            -- the savepoint back, the next BEGIN re-enters this
            -- statement, and the LATERAL sees freshly-committed rows
            -- from concurrent transactions. The per-repo CASE rules
            -- (virtual clamp, FROM/TO-leaf own_* deltas, in_from / in_to
            -- in/outgoing deltas, LCA-cancellation passthrough) are
            -- VERBATIM from migration 20260430070249 — only the data
            -- source moved from pg_temp.tmp_stock_deltas to the inline
            -- LATERAL.
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
                a.id,
                p_item_id,
                v_movement_id,
                -- Quantity: at CREATE time the baseline is preserved.
                -- The actual delta is applied later by ExecuteItemMovement
                -- via applyItemMovementStockDelta. Mirrors the Go path:
                -- simulateRepositoryStockMapWalk never assigns Quantity,
                -- it only bumps the pending Incoming/Outgoing counters.
                -- Virtual repos are still clamped to 0 (FINDINGS #3).
                CASE
                    WHEN r.virtual_repo THEN 0
                    ELSE COALESCE(s.quantity, 0)
                END,
                -- IncomingStock: only the TO-walk pushes +q; the LCA
                -- cancellation is handled implicitly because the LCA
                -- is in both walks and their deltas net to zero on
                -- non-leaf nodes. On the LCA itself we keep the base.
                CASE
                    WHEN a.in_to AND NOT a.in_from THEN GREATEST(COALESCE(s.incoming_stock, 0) + p_quantity, 0)
                    ELSE COALESCE(s.incoming_stock, 0)
                END,
                CASE
                    WHEN a.in_from AND NOT a.in_to THEN GREATEST(COALESCE(s.outgoing_stock, 0) + p_quantity, 0)
                    ELSE COALESCE(s.outgoing_stock, 0)
                END,
                -- own_quantity: at CREATE time the baseline is preserved.
                -- The actual delta on the FROM / TO leaf is applied later
                -- by ExecuteItemMovement via applyItemMovementStockDelta.
                -- Mirrors the Go path: simulateRepositoryStockMapWalk
                -- never assigns OwnQuantity at create-time, only the
                -- OwnIncoming / OwnOutgoing reservations below.
                -- Virtual leaves are still clamped to 0.
                CASE
                    WHEN r.virtual_repo AND (a.id = p_from_id OR a.id = p_to_id) THEN 0
                    ELSE COALESCE(s.own_quantity, 0)
                END,
                CASE
                    WHEN a.id = p_to_id THEN GREATEST(COALESCE(s.own_incoming_stock, 0) + p_quantity, 0)
                    ELSE COALESCE(s.own_incoming_stock, 0)
                END,
                CASE
                    WHEN a.id = p_from_id THEN GREATEST(COALESCE(s.own_outgoing_stock, 0) + p_quantity, 0)
                    ELSE COALESCE(s.own_outgoing_stock, 0)
                END,
                -- version = current_max + 1 per (tenant, repo, item).
                -- A unique_violation here is the OCC retry point.
                COALESCE(
                    (SELECT MAX(version)
                     FROM   stocks s3
                     WHERE  s3.tenant_id = p_tenant_id
                       AND  s3.repository_id = a.id
                       AND  s3.item_id = p_item_id),
                    -1
                ) + 1
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
            ) s ON TRUE
            -- Skip ancestors above the LCA — the §3 trim already
            -- removed them, but the explicit guard mirrors the Go-path
            -- skip-noop branch in insertStockMapWithVersions and keeps
            -- the proc safe if the trim is ever loosened.
            WHERE  a.in_from OR a.in_to;

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
'against stock_tenant_id_repository_id_item_id_version. Each retry '
're-evaluates the inline LATERAL baseline against the freshly-visible '
'stocks state, so a concurrent committer''s row is incorporated into the '
'next INSERT attempt — no stale-baseline race. Returns the movement id.';
