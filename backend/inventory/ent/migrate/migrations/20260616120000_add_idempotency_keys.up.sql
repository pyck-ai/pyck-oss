-- Idempotency keys table for Stripe-style Idempotency-Key support on
-- GraphQL mutations (pyck#1123). The row is written inside the mutation
-- transaction by backend/common/gqltx so the mutation and its cached
-- response either commit together or roll back together.

CREATE TABLE IF NOT EXISTS inventory.idempotency_keys (
    id uuid NOT NULL,
    key character varying(255) NOT NULL,
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    operation_name character varying NOT NULL,
    operation_checksum bytea NOT NULL,
    status character varying NOT NULL DEFAULT 'in_flight',
    response bytea NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id),
    CONSTRAINT idempotency_keys_status_check
        CHECK (status IN ('in_flight', 'committed')),
    CONSTRAINT idempotency_keys_checksum_len_check
        CHECK (octet_length(operation_checksum) = 32)
);

-- Lookup + uniqueness in one shot: PreCheck reads by this key tuple, and
-- the in-tx InsertInFlight relies on the UNIQUE constraint to serialize
-- concurrent writers across replicas.
CREATE UNIQUE INDEX IF NOT EXISTS idempotency_keys_tenant_user_key
    ON inventory.idempotency_keys (tenant_id, user_id, key);

-- Janitor scan path: DELETE WHERE status = 'committed' AND created_at < cutoff.
CREATE INDEX IF NOT EXISTS idempotency_keys_committed_created
    ON inventory.idempotency_keys (created_at)
    WHERE status = 'committed';

COMMENT ON TABLE inventory.idempotency_keys IS
    'Stripe-style idempotency records for GraphQL mutations; written inside the mutation tx by gqltx.';
COMMENT ON COLUMN inventory.idempotency_keys.operation_checksum IS
    'sha256 over operation_name + canonical_json(variables); rejects key reuse with different payloads (422).';
COMMENT ON COLUMN inventory.idempotency_keys.status IS
    'Lifecycle: in_flight while the mutation tx is open, committed once the response is cached.';
COMMENT ON COLUMN inventory.idempotency_keys.response IS
    'Serialized graphql.Response replayed verbatim on cache hits. NULL while in_flight.';
