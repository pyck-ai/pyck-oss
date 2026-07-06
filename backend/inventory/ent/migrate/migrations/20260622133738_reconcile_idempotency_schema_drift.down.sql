-- reverse: set comment to column: "response" on table: "idempotency_keys"
COMMENT ON COLUMN "idempotency_keys"."response" IS 'Serialized graphql.Response replayed verbatim on cache hits. NULL while in_flight.';
-- reverse: set comment to column: "status" on table: "idempotency_keys"
COMMENT ON COLUMN "idempotency_keys"."status" IS 'Lifecycle: in_flight while the mutation tx is open, committed once the response is cached.';
-- reverse: set comment to column: "operation_checksum" on table: "idempotency_keys"
COMMENT ON COLUMN "idempotency_keys"."operation_checksum" IS 'sha256 over operation_name + canonical_json(variables); rejects key reuse with different payloads (422).';
-- reverse: set comment to table: "idempotency_keys"
COMMENT ON TABLE "idempotency_keys" IS 'Stripe-style idempotency records for GraphQL mutations; written inside the mutation tx by gqltx.';
