-- Drop create_item_movement_proc. Signature must match the up migration
-- exactly so PostgreSQL can locate the overload.
DROP FUNCTION IF EXISTS inventory.create_item_movement_proc(
    uuid, uuid, uuid, uuid, bigint, text, uuid, uuid, int, uuid, text, jsonb, uuid, uuid
);
