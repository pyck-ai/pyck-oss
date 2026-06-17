package gqltx

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/idempotency"
	"github.com/pyck-ai/pyck/backend/common/log"
)

// Option configures NewMiddleware behavior beyond the four required
// parameters. Use the With* helpers below to construct values; the
// underlying type is intentionally unexported so callers cannot reach
// past the public surface.
type Option func(*options)

// leaseKey is the context key under which a successful PreCheck stores
// the lease produced for ActionProceed. handleMutationWithTx consults it
// to decide whether to write an idempotency row inside the mutation
// transaction.
type leaseKey struct{}

// WithIdempotency wires the [idempotency.Store] and [idempotency.AuthLookup]
// the middleware consults on every mutation. When the store is nil the
// middleware short-circuits to its existing behavior, so this option is
// genuinely opt-in.
func WithIdempotency(store idempotency.Store, auth idempotency.AuthLookup) Option {
	return func(o *options) {
		o.IdempotencyStore = store
		o.IdempotencyAuth = auth
	}
}

// WithIdempotencyMaxResponseBytes overrides the maximum serialized response
// size the middleware will cache for a committed idempotency key. A value
// <= 0 leaves the default ([idempotency.DefaultMaxResponseBytes], 1 MiB) in
// effect. Wire this from the per-service config so a service with
// legitimately larger responses can raise its ceiling without affecting
// other services; responses over the cap still roll back and surface a 413
// rather than degrading to a non-idempotent commit.
func WithIdempotencyMaxResponseBytes(maxBytes int) Option {
	return func(o *options) {
		o.IdempotencyMaxResponseBytes = maxBytes
	}
}

// withLease attaches a lease to ctx for the rest of the request lifecycle.
func withLease(ctx context.Context, lease *idempotency.Lease) context.Context {
	if lease == nil {
		return ctx
	}
	return context.WithValue(ctx, leaseKey{}, lease)
}

// leaseFromContext returns the lease attached by withLease, or nil if
// none was set (idempotency disabled or PreCheck returned ActionSkip).
func leaseFromContext(ctx context.Context) *idempotency.Lease {
	if v, ok := ctx.Value(leaseKey{}).(*idempotency.Lease); ok {
		return v
	}
	return nil
}

// responseTooLargeResponse builds the structured 413 response surfaced to
// the client when [idempotency.SerializeResponse] reports the body
// exceeds the configured response cap. The shape mirrors what
// [idempotency.PreCheck] short-circuits with, so client error-handling
// code can use one branch for all idempotency-driven rejections.
func responseTooLargeResponse() *graphql.Response {
	return &graphql.Response{
		Errors: gqlerror.List{
			&gqlerror.Error{
				Message: "Idempotency-Key response exceeds the maximum cacheable size; split the mutation into smaller calls",
				Extensions: map[string]any{
					"code":       idempotency.CodeResponseTooLarge,
					"httpStatus": http.StatusRequestEntityTooLarge,
				},
			},
		},
	}
}

// idempotencyInternalErrorResponse builds the generic 500 response used
// when an in-tx idempotency write fails (SerializeResponse with a non-
// too-large error, or Store.MarkCommitted returning an unexpected error).
// Shape mirrors the short-circuit responses produced by
// [idempotency.PreCheck] so clients can branch on a single
// extensions.code value; the underlying wrapped error is logged via
// log.ForContext and intentionally NOT surfaced on the wire.
func idempotencyInternalErrorResponse() *graphql.Response {
	return &graphql.Response{
		Errors: gqlerror.List{
			&gqlerror.Error{
				Message: "internal error finalizing idempotency record",
				Extensions: map[string]any{
					"code":       idempotency.CodeStoreError,
					"httpStatus": http.StatusInternalServerError,
				},
			},
		},
	}
}

// insertInFlight writes the lease's in-flight idempotency row inside
// the current attempt's transaction. A nil return means the row was
// inserted and the mutation may execute; a non-nil handler
// short-circuits the attempt with one of:
//
//   - the race-winner's replayed response (concurrent insert won; the
//     loser re-looks-up via ResolveRace so both paths emit identical
//     bodies),
//   - a bounded OCC retry recursion (retryable PG error — under
//     SERIALIZABLE a concurrent same-key INSERT can surface as 40001
//     instead of 23505; the retry re-runs InsertInFlight and then hits
//     the clean UNIQUE violation → ResolveRace → replay), or
//   - the terminal structured 500.
//
// The retry budget is decremented through the ctx so recursion is
// bounded (gqltx is the only NumRetriesFromContext consumer). Rolls
// back tx on every failure path.
func (m *Middleware[T]) insertInFlight(
	ns string,
	ctx context.Context,
	tx Tx,
	lease *idempotency.Lease,
	attempts *int,
	maxRetries int,
	next graphql.OperationHandler,
) graphql.ResponseHandler {
	err := m.Options.IdempotencyStore.InsertInFlight(ctx, idempotency.Record{
		Key:               lease.Key,
		TenantID:          lease.TenantID,
		UserID:            lease.UserID,
		OperationName:     lease.OperationName,
		OperationChecksum: lease.OperationChecksum,
		Status:            idempotency.StatusInFlight,
	})
	if err == nil {
		return nil
	}
	_ = tx.Rollback()

	if errors.Is(err, idempotency.ErrUniqueViolation) {
		raceResult := idempotency.ResolveRace(ctx, m.Options.IdempotencyStore, lease)
		idempotencyOutcomes.WithLabelValues(ns, idempotencyOutcomeLabel(raceResult, "race_")).Inc()
		return graphql.OneShot(raceResult.Response)
	}

	if db.ErrIsRetryable(err) && maxRetries > 0 {
		*attempts++
		log.ForContext(ctx).Warn().
			Err(err).
			Str("ns", ns).
			Str("key", lease.Key).
			Int("retries_left", maxRetries-1).
			Msg("idempotency: InsertInFlight hit retryable PG error; retrying transaction")
		time.Sleep(db.GetSleepDuration(*attempts))
		return m.handleMutationWithTx(db.WithMaxRetries(ctx, maxRetries-1), next)
	}

	log.ForContext(ctx).Error().
		Err(err).
		Str("ns", ns).
		Str("key", lease.Key).
		Msg("idempotency: InsertInFlight failed (non-UNIQUE)")
	txFailureCounter.WithLabelValues(ns).Inc()
	idempotencyOutcomes.WithLabelValues(ns, "internal_error").Inc()
	return graphql.OneShot(idempotencyInternalErrorResponse())
}

// persistIdempotencyResponse serializes r and marks the lease's row
// committed, both inside the mutation transaction — the crux of the
// at-most-once guarantee. A nil return means the caller may proceed to
// tx.Commit; a non-nil response short-circuits with a structured 413
// (response over the configured cap), a bounded OCC retry recursion
// (retryable PG error from the UPDATE — the rolled-back attempt's
// in-flight row is gone, so the retry re-inserts cleanly), or the
// terminal structured 500. Rolls back tx on every failure path.
//
// Known limitation — replays return the PRE-PATCH response body.
// SerializeResponse runs here, inside the mutation transaction and before
// tx.Commit, but response patches registered via AddResponsePatch (e.g.
// the workflow-reply IDs injected by NewWorkflowReplyMiddleware) run AFTER
// commit so they can incorporate post-commit data. The cached body is
// therefore the version without those patches, and a later replay of the
// same key returns state frozen at original-execution time, omitting any
// post-commit details.
//
// This is inherent to the atomic in-transaction design, not a fixable
// ordering bug: the committed marker MUST be written inside the mutation
// tx to keep the at-most-once guarantee, while patch data only exists
// after commit (it can block on a NATS reply), so the two cannot both be
// captured in one write. The limitation is general — any operation can
// register post-commit patches — so it is not restricted to workflow
// mutations, and a workflow-only guard would not reliably help. The real
// fix is to stop patching responses post-commit and instead return a
// handle the client looks up; that is tracked in #1298, after which this
// limitation disappears.
func (m *Middleware[T]) persistIdempotencyResponse(
	ns string,
	ctx context.Context,
	tx Tx,
	r *graphql.Response,
	lease *idempotency.Lease,
	attempts *int,
	maxRetries int,
	next graphql.OperationHandler,
	callCtx context.Context,
) *graphql.Response {
	body, err := idempotency.SerializeResponse(r, m.Options.IdempotencyMaxResponseBytes)
	if err != nil {
		_ = tx.Rollback()
		txFailureCounter.WithLabelValues(ns).Inc()
		if errors.Is(err, idempotency.ErrResponseTooLarge) {
			// Surface a structured 413 so the client knows to split the
			// mutation. Preserves the at-most-once contract by rolling
			// back rather than caching a truncated body.
			log.ForContext(ctx).Warn().
				Err(err).
				Str("ns", ns).
				Str("key", lease.Key).
				Msg("idempotency: response exceeds configured cap; mutation rolled back")
			idempotencyOutcomes.WithLabelValues(ns, "too_large").Inc()
			return responseTooLargeResponse()
		}
		log.ForContext(ctx).Error().
			Err(err).
			Str("ns", ns).
			Str("key", lease.Key).
			Msg("idempotency: serialize response failed")
		idempotencyOutcomes.WithLabelValues(ns, "internal_error").Inc()
		return idempotencyInternalErrorResponse()
	}

	if err := m.Options.IdempotencyStore.MarkCommitted(ctx, lease.Key, lease.TenantID, lease.UserID, body); err != nil {
		_ = tx.Rollback()
		if db.ErrIsRetryable(err) && maxRetries > 0 {
			// Mirror insertInFlight's accounting: bump the shared attempt
			// counter and back off proportionally so the two in-tx write
			// paths produce the same predictable backoff curve.
			*attempts++
			log.ForContext(ctx).Warn().
				Err(err).
				Str("ns", ns).
				Str("key", lease.Key).
				Int("retries_left", maxRetries-1).
				Msg("idempotency: MarkCommitted hit retryable PG error; retrying transaction")
			time.Sleep(db.GetSleepDuration(*attempts))
			return m.handleMutationWithTx(db.WithMaxRetries(ctx, maxRetries-1), next)(callCtx)
		}
		txFailureCounter.WithLabelValues(ns).Inc()
		log.ForContext(ctx).Error().
			Err(err).
			Str("ns", ns).
			Str("key", lease.Key).
			Msg("idempotency: MarkCommitted failed")
		idempotencyOutcomes.WithLabelValues(ns, "internal_error").Inc()
		return idempotencyInternalErrorResponse()
	}

	return nil
}

// idempotencyOutcomeLabel maps an [idempotency.Result] to the value of the
// "outcome" Prometheus label. The optional prefix is used by race
// resolutions so operators can split race-loser counts from primary
// PreCheck counts (pass "race_" for ResolveRace results, "" otherwise).
func idempotencyOutcomeLabel(r idempotency.Result, prefix string) string {
	var base string
	switch r.Action {
	case idempotency.ActionSkip:
		base = "skip"
	case idempotency.ActionProceed:
		base = "proceed"
	case idempotency.ActionReplay:
		base = "replay"
	case idempotency.ActionShortCircuit:
		switch r.Status {
		case http.StatusBadRequest:
			base = "bad_request"
		case http.StatusConflict:
			base = "in_flight"
		case http.StatusUnprocessableEntity:
			base = "mismatch"
		case http.StatusRequestEntityTooLarge:
			base = "too_large"
		default:
			base = "internal_error"
		}
	default:
		base = "unknown"
	}
	return prefix + base
}
