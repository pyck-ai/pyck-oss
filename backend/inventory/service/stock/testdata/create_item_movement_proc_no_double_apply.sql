-- Postgres-only regression for create_item_movement_proc (migration
-- 20260430070249). Pins the same create-time contract that
-- TestCreateItemMovement_PreservesQuantityAtCreate exercises on the
-- SQLite Go path, but on the Postgres proc path that the Go tests cannot
-- reach (the dispatch in CreateItemMovement only routes to the proc when
-- s.dbDialect == dialect.Postgres).
--
-- Contract: at CREATE time the proc must update only the pending
-- counters (incoming_stock / outgoing_stock and their own_* counterparts).
-- It MUST NOT touch quantity or own_quantity — those are the
-- responsibility of EXECUTE (ApplyItemMovementStockDelta in
-- ExecuteItemMovement).
--
-- Failure mode this guards against: an earlier revision of the proc
-- applied the FROM/TO delta to quantity / own_quantity at CREATE, so a
-- later EXECUTE re-applied the delta and surfaced as
-- "stock underflow: quantity would be -1" on the FROM leaf — exactly
-- the failure observed in pyck-projects/hellmann TestHellmannWorkflows
-- on a clean tenant. Asserting the contract here pins the fix.
--
-- Usage (the proc must already be loaded; run after task generate or
-- after the migrations have been applied):
--
--   docker exec -i db psql -U admin -d pyck_dev \
--     < backend/inventory/service/stock/testdata/create_item_movement_proc_no_double_apply.sql
--
-- Cleanup is unconditional so the script is rerunnable.

SET search_path = inventory, public;

DO $test$
DECLARE
    v_tenant_id      uuid := gen_random_uuid();
    v_source_id      uuid := gen_random_uuid();
    v_target_id      uuid := gen_random_uuid();
    v_item_id        uuid := gen_random_uuid();
    v_seed_qty       bigint := 5;
    v_move_qty       bigint := 2;
    v_user_id        uuid := gen_random_uuid();
    v_movement_id    uuid;
    v_src_qty        bigint;
    v_src_own        bigint;
    v_src_out_pend   bigint;
    v_tgt_qty        bigint;
    v_tgt_own        bigint;
    v_tgt_in_pend    bigint;
BEGIN
    -- Source repo (a "pallet" — non-virtual, root). Seeded with stock so
    -- the proc's FROM-availability gate doesn't short-circuit.
    INSERT INTO repositories (id, tenant_id, name, type, virtual_repo, created_at, created_by)
    VALUES (v_source_id, v_tenant_id, 'TEST_SOURCE_' || v_source_id, 'static', false, now(), v_user_id);

    -- Target repo (a "box" — non-virtual, root, separate subtree).
    INSERT INTO repositories (id, tenant_id, name, type, virtual_repo, created_at, created_by)
    VALUES (v_target_id, v_tenant_id, 'TEST_TARGET_' || v_target_id, 'static', false, now(), v_user_id);

    INSERT INTO items (id, tenant_id, sku, created_at, created_by)
    VALUES (v_item_id, v_tenant_id, 'TEST-SKU-' || v_item_id, now(), v_user_id);

    INSERT INTO stocks (id, tenant_id, repository_id, item_id,
                        quantity, own_quantity,
                        incoming_stock, outgoing_stock,
                        own_incoming_stock, own_outgoing_stock,
                        version, created_at, created_by)
    VALUES (gen_random_uuid(), v_tenant_id, v_source_id, v_item_id,
            v_seed_qty, v_seed_qty, 0, 0, 0, 0, 0, now(), v_user_id);

    v_movement_id := inventory.create_item_movement_proc(
        p_tenant_id      => v_tenant_id,
        p_item_id        => v_item_id,
        p_from_id        => v_source_id,
        p_to_id          => v_target_id,
        p_quantity       => v_move_qty,
        p_handler        => 'test',
        p_collection_id  => '00000000-0000-0000-0000-000000000000'::uuid,
        p_order_id       => NULL,
        p_position       => 0,
        p_data_type_id   => NULL,
        p_data_type_slug => NULL,
        p_data           => NULL,
        p_created_by     => v_user_id,
        p_movement_id    => NULL
    );

    SELECT quantity, own_quantity, own_outgoing_stock
    INTO   v_src_qty, v_src_own, v_src_out_pend
    FROM   stocks
    WHERE  tenant_id = v_tenant_id
      AND  repository_id = v_source_id
      AND  item_id = v_item_id
      AND  deleted_at IS NULL
    ORDER  BY created_at DESC, version DESC
    LIMIT  1;

    SELECT quantity, own_quantity, own_incoming_stock
    INTO   v_tgt_qty, v_tgt_own, v_tgt_in_pend
    FROM   stocks
    WHERE  tenant_id = v_tenant_id
      AND  repository_id = v_target_id
      AND  item_id = v_item_id
      AND  deleted_at IS NULL
    ORDER  BY created_at DESC, version DESC
    LIMIT  1;

    -- Contract assertions. Each RAISE EXCEPTION makes psql exit non-zero.

    IF v_src_qty <> v_seed_qty THEN
        RAISE EXCEPTION 'FAIL: source.quantity should be % (unchanged at CREATE), got %',
            v_seed_qty, v_src_qty;
    END IF;
    IF v_src_own <> v_seed_qty THEN
        RAISE EXCEPTION 'FAIL: source.own_quantity should be % (unchanged at CREATE), got %',
            v_seed_qty, v_src_own;
    END IF;
    IF v_src_out_pend <> v_move_qty THEN
        RAISE EXCEPTION 'FAIL: source.own_outgoing_stock should be % (reservation), got %',
            v_move_qty, v_src_out_pend;
    END IF;

    IF v_tgt_qty <> 0 THEN
        RAISE EXCEPTION 'FAIL: target.quantity should be 0 (unchanged at CREATE), got %',
            v_tgt_qty;
    END IF;
    IF v_tgt_own <> 0 THEN
        RAISE EXCEPTION 'FAIL: target.own_quantity should be 0 (unchanged at CREATE), got %',
            v_tgt_own;
    END IF;
    IF v_tgt_in_pend <> v_move_qty THEN
        RAISE EXCEPTION 'FAIL: target.own_incoming_stock should be % (reservation), got %',
            v_move_qty, v_tgt_in_pend;
    END IF;

    RAISE NOTICE 'PASS: create_item_movement_proc honors the create-time contract';

EXCEPTION
    WHEN OTHERS THEN
        DELETE FROM stocks WHERE tenant_id = v_tenant_id;
        DELETE FROM item_movements WHERE tenant_id = v_tenant_id;
        DELETE FROM items WHERE tenant_id = v_tenant_id;
        DELETE FROM repositories WHERE tenant_id = v_tenant_id;
        RAISE;
END;
$test$;

DELETE FROM inventory.stocks
WHERE repository_id IN (
    SELECT id FROM inventory.repositories
    WHERE name LIKE 'TEST_SOURCE_%' OR name LIKE 'TEST_TARGET_%'
);
DELETE FROM inventory.item_movements
WHERE from_id IN (SELECT id FROM inventory.repositories WHERE name LIKE 'TEST_SOURCE_%')
   OR to_id   IN (SELECT id FROM inventory.repositories WHERE name LIKE 'TEST_TARGET_%');
DELETE FROM inventory.items WHERE sku LIKE 'TEST-SKU-%';
DELETE FROM inventory.repositories WHERE name LIKE 'TEST_SOURCE_%' OR name LIKE 'TEST_TARGET_%';
