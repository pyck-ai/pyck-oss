-- Restore inventory.create_item_movement_proc to the body installed by
-- migration 20260430070249_create_item_movement_proc.up.sql. This is a
-- verbatim copy of that up-migration's CREATE OR REPLACE body, used to
-- roll back the inline-LATERAL refactor introduced by the matching .up.sql.

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

    DROP TABLE IF EXISTS pg_temp.tmp_ancestors;
    CREATE TEMP TABLE pg_temp.tmp_ancestors ON COMMIT DROP AS
    WITH RECURSIVE walk(id, parent_id, depth, side) AS (
        SELECT id, parent_id, 0, 1
        FROM   repositories
        WHERE  tenant_id = p_tenant_id AND id = p_from_id AND deleted_at IS NULL
        UNION ALL
        SELECT id, parent_id, 0, 2
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

    SELECT id INTO v_lca_id
    FROM   pg_temp.tmp_ancestors
    WHERE  in_from AND in_to
    ORDER  BY GREATEST(from_depth, to_depth) ASC, from_depth ASC
    LIMIT  1;

    IF v_lca_id IS NOT NULL THEN
        DELETE FROM pg_temp.tmp_ancestors
        WHERE (in_from AND from_depth > (SELECT from_depth FROM pg_temp.tmp_ancestors WHERE id = v_lca_id))
           OR (in_to   AND to_depth   > (SELECT to_depth   FROM pg_temp.tmp_ancestors WHERE id = v_lca_id))
           OR (NOT in_from AND NOT in_to);
    END IF;

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

    LOOP
        BEGIN
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
                CASE
                    WHEN d.is_virtual THEN 0
                    ELSE d.base_quantity
                END,
                CASE
                    WHEN d.in_to AND NOT d.in_from THEN GREATEST(d.base_incoming + p_quantity, 0)
                    ELSE d.base_incoming
                END,
                CASE
                    WHEN d.in_from AND NOT d.in_to THEN GREATEST(d.base_outgoing + p_quantity, 0)
                    ELSE d.base_outgoing
                END,
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
                COALESCE(
                    (SELECT MAX(version)
                     FROM   stocks s3
                     WHERE  s3.tenant_id = p_tenant_id
                       AND  s3.repository_id = d.repository_id
                       AND  s3.item_id = p_item_id),
                    -1
                ) + 1
            FROM pg_temp.tmp_stock_deltas d
            WHERE NOT (
                d.in_from = FALSE AND d.in_to = FALSE
            );

            RETURN v_movement_id;

        EXCEPTION
            WHEN unique_violation THEN
                IF SQLERRM NOT ILIKE '%stock_tenant_id_repository_id_item_id_version%' THEN
                    RAISE;
                END IF;
                v_retries := v_retries + 1;
                IF v_retries > v_max_retries THEN
                    RAISE;
                END IF;
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
