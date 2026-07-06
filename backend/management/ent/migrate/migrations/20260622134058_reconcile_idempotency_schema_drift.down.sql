-- reverse: set comment to table: "idempotency_keys"
COMMENT ON TABLE "idempotency_keys" IS 'Stripe-style idempotency records for GraphQL mutations; written inside the mutation tx by gqltx.';
