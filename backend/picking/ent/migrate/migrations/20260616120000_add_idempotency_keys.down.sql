-- Rollback idempotency keys table.

DROP INDEX IF EXISTS picking.idempotency_keys_committed_created;
DROP INDEX IF EXISTS picking.idempotency_keys_tenant_user_key;
DROP TABLE IF EXISTS picking.idempotency_keys;
