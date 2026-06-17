// Package idempotency implements Stripe-style idempotency keys for GraphQL
// mutations.
//
// Clients opt in by sending an Idempotency-Key HTTP header on a GraphQL
// mutation request. The server then guarantees the mutation is executed
// at most once per successful DB commit for the tuple
// (key, tenant_id, user_id), replaying the cached response for any retry
// within the TTL window.
//
// The package is intentionally split into three layers:
//
//   - Pure helpers (Key parsing, OperationChecksum, response
//     serialization, sentinel errors and typed Code* constants) live
//     here and have no DB dependency.
//   - The Store interface abstracts persistence; the production
//     implementation lives per service so it can use that service's Ent
//     client (idempotency rows are written inside the same DB transaction
//     as the mutation).
//   - The gqltx middleware orchestrates PreCheck before opening a
//     transaction and in-transaction record writes (InsertInFlight,
//     MarkCommitted, ResolveRace) during execution.
//
// All outcomes are surfaced as HTTP 200 with the result encoded in
// errors[].extensions.{code, httpStatus} on the GraphQL response.
// See knowledge/1123-idempotency-keys.md for the full architecture
// and the design-decision rationale.
package idempotency
