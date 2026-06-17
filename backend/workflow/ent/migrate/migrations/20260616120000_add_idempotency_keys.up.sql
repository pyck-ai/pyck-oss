-- Idempotency keys table for Stripe-style Idempotency-Key support on
-- GraphQL mutations (pyck#1123). The row is written inside the mutation
-- transaction by backend/common/gqltx so the mutation and its cached
-- response either commit together or roll back together.

CREATE TABLE IF NOT EXISTS workflow.idempotency_keys (
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

CREATE UNIQUE INDEX IF NOT EXISTS idempotency_keys_tenant_user_key
    ON workflow.idempotency_keys (tenant_id, user_id, key);

CREATE INDEX IF NOT EXISTS idempotency_keys_committed_created
    ON workflow.idempotency_keys (created_at)
    WHERE status = 'committed';

COMMENT ON TABLE workflow.idempotency_keys IS
    'Stripe-style idempotency records for GraphQL mutations; written inside the mutation tx by gqltx.';
