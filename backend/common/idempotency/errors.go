package idempotency

import "errors"

// Error codes returned in errors[].extensions.code on idempotency
// short-circuit responses. Operators triage by these strings; keep them
// stable. The companion extensions.httpStatus int (set by [shortCircuit])
// carries the semantic HTTP status — see knowledge file 1123 for why
// the wire status is always 200.
const (
	// CodeInvalidKey — the Idempotency-Key header value failed format
	// validation (currently: > 255 characters). Status 400.
	CodeInvalidKey = "INVALID_IDEMPOTENCY_KEY"

	// CodeInvalidRequest — the request reached PreCheck without a
	// parsed GraphQL operation. Defensive; should not happen in
	// production. Status 400.
	CodeInvalidRequest = "INVALID_IDEMPOTENCY_REQUEST"

	// CodeInvalidVariables — request variables could not be canonical-
	// JSON-encoded so a stable checksum can't be computed. Programmer
	// error path (gqlgen validates against the schema first). Status 400.
	CodeInvalidVariables = "INVALID_VARIABLES"

	// CodeMissingOpName — Idempotency-Key + no operationName. Issue
	// #1123 acceptance criterion. Status 400.
	CodeMissingOpName = "MISSING_OPERATION_NAME"

	// CodeMultiField — Idempotency-Key + > 1 top-level mutation field.
	// Issue #1123 known limitation: per-service tx model cannot give
	// at-most-once across multiple fields. Status 400.
	CodeMultiField = "MULTI_FIELD_MUTATION"

	// CodeUnauthenticated — Idempotency-Key + no authenticated tenant
	// or user. Confirmed design choice (vs silently ignoring). Status 400.
	CodeUnauthenticated = "UNAUTHENTICATED"

	// CodeOperationMismatch — Idempotency-Key reused with a different
	// operation_name + variables checksum. Status 422.
	CodeOperationMismatch = "OPERATION_MISMATCH"

	// CodeInFlight — Idempotency-Key matches an existing record still
	// in StatusInFlight. Status 409.
	//
	// SHOULD NEVER HAPPEN in correct code: because the InsertInFlight
	// and the UPDATE-to-committed happen in the same Postgres tx, a
	// concurrent reader cannot observe status='in_flight'. The losing
	// concurrent attempt blocks on the UNIQUE constraint and then either
	// replays the winner's committed row or proceeds (winner rolled
	// back). Reachable only via DB tampering or a future refactor that
	// splits the INSERT and the UPDATE across transactions. Treat a
	// non-zero counter as an alert. See knowledge file 1123 §G10.
	CodeInFlight = "IDEMPOTENCY_IN_FLIGHT"

	// CodeRaceGhost — post-UNIQUE-violation Lookup returned ErrNotFound.
	// SHOULD NEVER HAPPEN: a UNIQUE violation implies the conflicting
	// row exists. Non-zero counter indicates either a concurrent prune
	// race or a Postgres visibility issue. Page on it. Status 500.
	CodeRaceGhost = "IDEMPOTENCY_RACE_GHOST"

	// CodeStoreError — the underlying [Store] returned an unexpected
	// error during Lookup. Bubbled to the client as a 500 (without the
	// internal err.Error() text, which is logged separately).
	CodeStoreError = "IDEMPOTENCY_STORE_ERROR"

	// CodeCacheCorrupt — the stored response bytes failed JSON decoding
	// during replay. Indicates the row was tampered with or a serialization
	// bug shipped a malformed body. Status 500.
	CodeCacheCorrupt = "IDEMPOTENCY_CACHE_CORRUPT"

	// CodeUnknownStatus — the stored record has a status value outside
	// the documented enum. Schema-evolution edge; impossible from this
	// codebase but defends against direct DB tampering. Status 500.
	CodeUnknownStatus = "IDEMPOTENCY_UNKNOWN_STATUS"

	// CodeResponseTooLarge — the GraphQL response exceeded the service's
	// configured response cap (see [DefaultMaxResponseBytes]) and was
	// rejected pre-commit to keep the
	// idempotency_keys row size bounded. Clients must split the mutation
	// into smaller calls. Status 413.
	CodeResponseTooLarge = "RESPONSE_TOO_LARGE"
)

// ErrNotFound is returned by [Store.Lookup] when no record exists for the
// given (key, tenant, user) tuple.
var ErrNotFound = errors.New("idempotency record not found")

// ErrUniqueViolation is returned by [Store.InsertInFlight] when a row with
// the same (key, tenant, user) already exists. Callers must roll back the
// surrounding transaction and re-route to the [ResolveRace] path.
var ErrUniqueViolation = errors.New("idempotency key already in use")

// ErrResponseTooLarge is returned by [SerializeResponse] when the marshaled
// GraphQL response would exceed the effective response cap (the per-request
// limit, or [DefaultMaxResponseBytes] when unset). The gqltx middleware
// catches this, rolls back the mutation, and surfaces a 413 to the client.
var ErrResponseTooLarge = errors.New("idempotency response too large")

// ErrNilResponse is returned by [SerializeResponse] when called with a nil
// *graphql.Response. Treated as a programmer error; should not happen in
// production because gqlgen always returns a non-nil response from a
// successful resolver.
var ErrNilResponse = errors.New("idempotency cannot serialize nil response")

// ErrNilOperation is returned by [OperationChecksum] when called with a nil
// operation definition. Treated as a programmer error; gqlgen always parses
// a non-nil operation before the middleware runs.
var ErrNilOperation = errors.New("idempotency cannot checksum nil operation")

// ErrEmptyResponse is returned by [DeserializeResponse] when the stored
// payload is empty. Callers should treat this as a cache miss (re-execute
// the mutation) rather than surfacing the empty body.
var ErrEmptyResponse = errors.New("idempotency cannot deserialize empty response")

// ErrNoTxInContext is returned by [Store.InsertInFlight] and
// [Store.MarkCommitted] when no `*ent.Tx` is present on the context.
// Indicates a wiring bug: those methods MUST be called from within the
// mutation transaction so the idempotency row is written atomically
// alongside the mutation.
var ErrNoTxInContext = errors.New("idempotency: no transaction in context")

// ErrNoInFlightRow is returned by [Store.MarkCommitted] when the
// expected in-flight row is not present at update time. Indicates the
// row was deleted out from under us (manual DB tampering, or the janitor
// pruned a stale `in_flight` row that should not have existed).
var ErrNoInFlightRow = errors.New("idempotency: no in-flight row to mark committed")
