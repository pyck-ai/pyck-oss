package idempotency

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// Action describes the resolution of a [PreCheck] for the current request.
type Action int

const (
	// ActionSkip means no idempotency header was provided; the surrounding
	// middleware must execute the mutation with its normal flow.
	ActionSkip Action = iota
	// ActionProceed means the header validated and the key was not seen
	// before; the surrounding middleware must open the mutation transaction
	// and call [InsertInFlight] before executing the mutation.
	ActionProceed
	// ActionReplay means a committed record was found; the surrounding
	// middleware must return Result.Response without executing the
	// mutation. The HTTP status is 200.
	ActionReplay
	// ActionShortCircuit means the request must be rejected with the HTTP
	// status in Result.Status and the body in Result.Response, without
	// executing the mutation.
	ActionShortCircuit
)

// Result is the outcome of [PreCheck]. Exactly one of {Response, Lease} is
// populated depending on Action: ActionReplay/ActionShortCircuit carry a
// Response; ActionProceed carries a Lease.
type Result struct {
	Action   Action
	Response *graphql.Response
	Status   int
	Lease    *Lease
}

// Lease is the validated parameters of an in-progress idempotency request.
// It is passed to the in-transaction hooks to insert and finalize the
// idempotency row.
type Lease struct {
	Key               string
	TenantID          uuid.UUID
	UserID            uuid.UUID
	OperationName     string
	OperationChecksum [32]byte
}

// AuthLookup returns the authenticated tenant and user for the current
// request, or false if no authentication is available. Supplied by the
// caller so this package does not import auth / request packages directly
// (which would create an import cycle via gqltx).
type AuthLookup func(ctx context.Context) (tenantID, userID uuid.UUID, ok bool)

// PreCheck inspects the request for an idempotency header and resolves
// what the caller should do. The store is consulted only when a key is
// present; passing a nil store with no header still returns ActionSkip.
func PreCheck(
	ctx context.Context,
	headers http.Header,
	oc *graphql.OperationContext,
	store Store,
	authLookup AuthLookup,
) Result {
	key, err := FromHeaders(headers)
	if err != nil {
		return shortCircuit(http.StatusBadRequest, CodeInvalidKey, err.Error())
	}
	if key == "" {
		return Result{Action: ActionSkip}
	}

	// From here on the header is present: every failure path returns an
	// HTTP-status-bearing GraphQL error rather than silently degrading.

	if oc == nil || oc.Operation == nil {
		return shortCircuit(http.StatusBadRequest, CodeInvalidRequest,
			"Idempotency-Key requires a parsed GraphQL operation")
	}

	if oc.Operation.Operation != ast.Mutation {
		// PreCheck is not expected to be called for queries, but be
		// defensive: an idempotency key on a non-mutation is silently
		// ignored per the issue spec.
		return Result{Action: ActionSkip}
	}

	if oc.Operation.Name == "" {
		return shortCircuit(http.StatusBadRequest, CodeMissingOpName,
			"Idempotency-Key requires operationName to be set on the request")
	}

	if topLevelMutationCount(oc.Operation) > 1 {
		return shortCircuit(http.StatusBadRequest, CodeMultiField,
			"Idempotency-Key cannot be used on requests with more than one top-level mutation field")
	}

	tenantID, userID, ok := authLookup(ctx)
	if !ok {
		return shortCircuit(http.StatusBadRequest, CodeUnauthenticated,
			"Idempotency-Key requires an authenticated request with a single tenant")
	}

	var fragments ast.FragmentDefinitionList
	if oc.Doc != nil {
		fragments = oc.Doc.Fragments
	}
	checksum, err := OperationChecksum(oc.Operation, fragments, oc.Variables)
	if err != nil {
		// Operation or variables can't be canonicalized — gqlgen already
		// validated against the schema so this is a programmer-error edge,
		// but failing loud is safer than mapping all failures to a single
		// sentinel checksum (which would let bad-payload retries
		// false-match each other).
		log.ForContext(ctx).Error().
			Err(err).
			Str("operation_name", oc.Operation.Name).
			Msg("idempotency: operation could not be canonicalized")
		return shortCircuit(http.StatusBadRequest, CodeInvalidVariables,
			"Idempotency-Key request could not be encoded")
	}

	rec, err := store.Lookup(ctx, key, tenantID, userID)
	switch {
	case errors.Is(err, ErrNotFound):
		// First sighting: caller proceeds with mutation + insert.
		return Result{
			Action: ActionProceed,
			Lease: &Lease{
				Key:               key,
				TenantID:          tenantID,
				UserID:            userID,
				OperationName:     oc.Operation.Name,
				OperationChecksum: checksum,
			},
		}
	case err != nil:
		log.ForContext(ctx).Error().
			Err(err).
			Str("operation_name", oc.Operation.Name).
			Msg("idempotency: store lookup failed")
		return shortCircuit(http.StatusInternalServerError, CodeStoreError,
			"internal error during idempotency check")
	}

	return resolveExisting(ctx, rec, checksum)
}

// ResolveRace resolves the post-UNIQUE-violation case. The gqltx
// middleware calls it when InsertInFlight returned [ErrUniqueViolation]
// — meaning a concurrent request inserted the same key between our
// PreCheck miss and our INSERT. The winner's row is now visible; we
// look it up and return the same Result PreCheck would have produced
// on a hit, so both paths emit identical response bodies.
//
// The lookup goes through [Store.LookupForResolve] (writer pool, never
// a replica): the UNIQUE violation proves the row exists on the
// primary, but a replica inside the replication-lag window may not
// have it yet — reading stale data here would misreport the race as
// CodeRaceGhost.
func ResolveRace(ctx context.Context, store Store, lease *Lease) Result {
	rec, err := store.LookupForResolve(ctx, lease.Key, lease.TenantID, lease.UserID)
	switch {
	case errors.Is(err, ErrNotFound):
		// SHOULD NEVER HAPPEN: UNIQUE violation implies the row exists.
		// Either the winner rolled back between the violation and our
		// lookup (which leaves no row to read, but also means the key
		// is retryable now) or there's a deeper consistency bug. Fail
		// loud rather than silently retry: an outer 500 lets the
		// client decide whether to retry with a fresh key. See
		// [CodeRaceGhost] for the operator runbook entry.
		log.ForContext(ctx).Error().
			Str("key", lease.Key).
			Str("tenant_id", lease.TenantID.String()).
			Str("user_id", lease.UserID.String()).
			Msg("idempotency: row vanished after UNIQUE violation (CodeRaceGhost)")
		return shortCircuit(http.StatusInternalServerError, CodeRaceGhost,
			"internal error during idempotency race resolution")
	case err != nil:
		log.ForContext(ctx).Error().
			Err(err).
			Str("key", lease.Key).
			Msg("idempotency: race-resolution lookup failed")
		return shortCircuit(http.StatusInternalServerError, CodeStoreError,
			"internal error during idempotency check")
	}
	return resolveExisting(ctx, rec, lease.OperationChecksum)
}

// resolveExisting maps an existing record + the request's checksum into
// the proper Result based on the record's status. Shared between
// [PreCheck] (cache-hit branch) and [ResolveRace] (post-violation
// branch) so both paths produce identical response bodies.
func resolveExisting(ctx context.Context, rec *Record, checksum [32]byte) Result {
	if rec.OperationChecksum != checksum {
		return shortCircuit(http.StatusUnprocessableEntity, CodeOperationMismatch,
			"Idempotency-Key was reused with a different operation or variables")
	}
	switch rec.Status {
	case StatusCommitted:
		resp, err := DeserializeResponse(rec.Response)
		if err != nil {
			// Cached body is corrupt — treat as a 500 rather than
			// silently re-executing, which would violate the
			// at-most-once guarantee under load.
			log.ForContext(ctx).Error().
				Err(err).
				Str("key", rec.Key).
				Int("response_bytes", len(rec.Response)).
				Msg("idempotency: cached response failed to decode")
			return shortCircuit(http.StatusInternalServerError, CodeCacheCorrupt,
				"internal error: cached idempotency response could not be decoded")
		}
		return Result{Action: ActionReplay, Response: resp, Status: http.StatusOK}
	case StatusInFlight:
		return shortCircuit(http.StatusConflict, CodeInFlight,
			"Another request with this Idempotency-Key is currently in flight")
	default:
		log.ForContext(ctx).Error().
			Str("key", rec.Key).
			Str("status", string(rec.Status)).
			Msg("idempotency: stored row has unknown status")
		return shortCircuit(http.StatusInternalServerError, CodeUnknownStatus,
			"internal error: stored idempotency record has an unknown status")
	}
}

// topLevelMutationCount counts top-level mutation fields on the
// operation, walking through inline fragments and fragment spreads so
// that a request like
//
//	mutation { ... on Mutation { a {id} b {id} } }
//
// is correctly counted as 2 (and rejected by the >1 check in PreCheck).
// Pre-this-fix the bare `for _, sel := range op.SelectionSet` counted
// each fragment as a single selection regardless of how many fields it
// contained, which let multi-field mutations slip through under a
// fragment wrapper and violate the single-mutation contract.
func topLevelMutationCount(op *ast.OperationDefinition) int {
	return countMutationFields(op.SelectionSet)
}

// countMutationFields walks a SelectionSet, recursing into inline
// fragments and fragment spreads, and returns the total number of
// top-level *ast.Field selections (i.e. actual mutation fields).
//
// Introspection fields (names starting with "__", e.g. __typename)
// are NOT counted: the GraphQL spec reserves the "__" prefix for
// introspection, those fields resolve without executing a mutation,
// and Apollo Client / Relay inject __typename at the operation root
// by default — counting it would reject every single-mutation
// request from a typical frontend with MULTI_FIELD_MUTATION.
func countMutationFields(set ast.SelectionSet) int {
	n := 0
	for _, sel := range set {
		switch s := sel.(type) {
		case *ast.Field:
			if !strings.HasPrefix(s.Name, "__") {
				n++
			}
		case *ast.InlineFragment:
			n += countMutationFields(s.SelectionSet)
		case *ast.FragmentSpread:
			if s.Definition != nil {
				n += countMutationFields(s.Definition.SelectionSet)
			}
		}
	}
	return n
}

// shortCircuit builds a short-circuit Result with a single GraphQL error
// carrying the given code and message, plus the HTTP status to surface.
func shortCircuit(status int, code, message string) Result {
	return Result{
		Action: ActionShortCircuit,
		Status: status,
		Response: &graphql.Response{
			Errors: gqlerror.List{
				&gqlerror.Error{
					Message: message,
					Extensions: map[string]any{
						"code":       code,
						"httpStatus": status,
					},
				},
			},
		},
	}
}
