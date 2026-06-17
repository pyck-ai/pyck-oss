package gqltx

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/idempotency"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/txid"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Tx is the minimal transaction interface (compatible with *ent.Tx).
type Tx interface {
	Commit() error
	Rollback() error
}

// TxClient represents a client that can start a transaction of type T with
// the given *sql.TxOptions. ent's generated *Client.BeginTx satisfies this.
//
// Mutations pass nil opts so the writer pool's default isolation applies
// (per-service: SERIALIZABLE everywhere, READ COMMITTED in inventory after
// Phase 6.4). Queries pass {ReadOnly: true, Isolation: REPEATABLE READ} so
// pgMultiDriver routes them to the reader pool with snapshot consistency
// across all statements in the request (FINDINGS §3.1).
type TxClient[T Tx] interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (T, error)
}

// InjectTxFunc injects a transaction into the context (e.g., ent.NewTxContext).
type InjectTxFunc[T Tx] func(ctx context.Context, tx T) context.Context

// NewMiddleware wires a transaction-capable client into the GraphQL server.
//
// The four positional arguments are required; extra behavior (e.g.
// idempotency support via WithIdempotency) is opt-in via the Option
// variadic so existing call sites stay source-compatible.
//
// Returns graphql.HandlerExtension because gqlgen's Server.Use takes
// that interface; returning the concrete *Middleware[T] would force
// every call site to wrap back to the interface.
//
//nolint:ireturn // gqlgen contract — see doc comment above.
func NewMiddleware[T Tx, C TxClient[T]](
	client C, inject InjectTxFunc[T], namespace string, defaultRetries int,
	extra ...Option,
) graphql.HandlerExtension {
	opts := options{
		DefaultRetries:   defaultRetries,
		MetricsNamespace: namespace,
	}
	for _, apply := range extra {
		apply(&opts)
	}
	return &Middleware[T]{
		Binder: binder[T]{
			Begin:  client.BeginTx,
			Inject: inject,
		},
		Options: opts,
	}
}

// binder declares how to begin a transaction and inject it into a context.
// This keeps the middleware decoupled from any specific ent package.
type binder[T Tx] struct {
	// Begin starts a new transaction with the given options. nil opts means
	// "writer at default isolation" (used for mutations); a non-nil opts with
	// ReadOnly: true is routed to the reader pool by pgMultiDriver.BeginTx
	// (Step 8.1) and used for queries.
	Begin func(ctx context.Context, opts *sql.TxOptions) (T, error)
	// Inject returns a context carrying the provided transaction (used by resolvers via ent.TxFromContext).
	Inject func(ctx context.Context, tx T) context.Context
}

// options configures middleware behavior.
type options struct {
	// DefaultRetries is the fallback when no retry count is present in the context.
	DefaultRetries int
	// MetricsNamespace tags Prometheus metrics (use service name).
	MetricsNamespace string
	// IdempotencyStore, if non-nil, activates Stripe-style Idempotency-Key
	// support on every mutation. See the idempotency package for the
	// per-service Ent-backed implementation.
	IdempotencyStore idempotency.Store
	// IdempotencyAuth resolves the authenticated tenant + user for a
	// request. Required when IdempotencyStore is set.
	IdempotencyAuth idempotency.AuthLookup
	// IdempotencyMaxResponseBytes caps the serialized response cached for a
	// committed idempotency key. Zero means use
	// idempotency.DefaultMaxResponseBytes; set per service via
	// WithIdempotencyMaxResponseBytes so a service with larger responses can
	// raise its own ceiling.
	IdempotencyMaxResponseBytes int
}

// Metrics (vectorized by namespace to support multi-service reuse).
var (
	txDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "gqltx_transaction_duration_seconds",
		Help: "Duration of GraphQL transactions",
	}, []string{"ns"})

	txTotalCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gqltx_transactions_total",
		Help: "Number of GraphQL transactions",
	}, []string{"ns"})

	txSuccessCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gqltx_transaction_successes_total",
		Help: "Number of successful GraphQL transactions",
	}, []string{"ns"})

	txFailureCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gqltx_transaction_failures_total",
		Help: "Number of failed GraphQL transactions",
	}, []string{"ns"})

	txRetriesPerTransaction = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gqltx_retries_per_transaction",
		Help:    "Number of retries per GraphQL transaction",
		Buckets: prometheus.LinearBuckets(0, 2, 10),
	}, []string{"ns"})

	// idempotencyOutcomes counts the resolution of every PreCheck and
	// ResolveRace pass through the middleware, labelled by outcome.
	// Race-loser paths use the "race_*" label space so they can be
	// summed or split out per ops preference. Outcomes are mutually
	// exclusive per request.
	idempotencyOutcomes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gqltx_idempotency_outcomes_total",
		Help: "Outcomes of GraphQL Idempotency-Key PreChecks / race resolutions",
	}, []string{"ns", "outcome"})
)

// Middleware starts one DB transaction per GraphQL mutation,
// retries on retryable DB errors, and orchestrates post-commit hooks.
//
// The Middleware struct is stateless; the mutex that serializes resolver
// access to the per-request *ent.Tx (and its underlying *sql.Conn) lives
// on the per-operation ResolverMiddleware closure installed by
// MutateOperationContext. Two unrelated requests therefore never contend.
type Middleware[T Tx] struct {
	Binder  binder[T]
	Options options
}

// ExtensionName implements graphql.HandlerExtension.
func (m *Middleware[T]) ExtensionName() string { return "Middleware" }

// Validate implements graphql.HandlerExtension.
func (m *Middleware[T]) Validate(graphql.ExecutableSchema) error { return nil }

// MutateOperationContext implements graphql.OperationContextMutator.
// It serializes field resolvers during mutations and queries to prevent
// concurrent access to the transaction. This is critical because ent.Tx
// (and sql.Tx) are not safe for concurrent use from multiple goroutines.
//
// The serializing mutex is allocated here, captured by the wrapping closure,
// and is therefore unique to this operation (and thus to the *ent.Tx that
// will be created by InterceptOperation for the same request). Concurrent
// edge resolvers within one request still share one mutex (correctness
// invariant from FINDINGS §3.1); two unrelated requests do not contend.
//
// Subscriptions are absent from every service's schema today; if one were
// added, the long-lived read-only tx would hold a reader-pool connection
// open for the lifetime of the subscription. Block subscriptions here so
// the foot-gun fails closed rather than silently exhausting the reader pool.
func (m *Middleware[T]) MutateOperationContext(ctx context.Context, oc *graphql.OperationContext) *gqlerror.Error {
	if oc.Operation == nil {
		return nil
	}

	op := oc.Operation.Operation
	if op != ast.Mutation && op != ast.Query {
		return nil
	}

	// Per-request mutex: lives on this closure, which is itself owned by the
	// per-operation ResolverMiddleware. gqlgen calls MutateOperationContext
	// once per operation, so each request gets its own mu.
	//
	// Note: *sync.Mutex (not RWMutex) is required because concurrent reads
	// on the same *sql.Tx / *sql.Conn are also unsafe even for a read-only
	// transaction (the underlying connection is still single-threaded).
	var mu sync.Mutex

	// Wrap the resolver middleware to serialize all field resolvers.
	// Without this, gqlgen's concurrent field resolution will cause multiple
	// goroutines to access the same transaction simultaneously, leading to
	// "driver: bad connection" errors. This applies to both mutations
	// (writer tx) and queries (read-only reader tx).
	previous := oc.ResolverMiddleware
	oc.ResolverMiddleware = func(ctx context.Context, next graphql.Resolver) (interface{}, error) {
		mu.Lock()
		defer mu.Unlock()
		return previous(ctx, next)
	}

	return nil
}

// InterceptOperation wraps GraphQL operations.
//
// Mutations get a writer-pool tx at the per-service default isolation
// (SERIALIZABLE except in inventory after Phase 6.4) and the full
// commit/rollback/retry pipeline.
//
// Queries get a reader-pool tx at REPEATABLE READ READ ONLY. Postgres
// treats REPEATABLE READ READ ONLY as a stable snapshot for the entire
// transaction, so every statement sees the same point-in-time view.
// Read-only txs cannot conflict, so the retry pipeline is unnecessary;
// the result is committed (a no-op rollback also works) once the
// response handler finishes.
//
// Subscriptions and nil operations bypass transactions entirely.
// No service ships a Subscription type today.
func (m *Middleware[T]) InterceptOperation(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
	oc := graphql.GetOperationContext(ctx)
	if oc.Operation == nil {
		return next(ctx)
	}

	switch oc.Operation.Operation {
	case ast.Mutation:
		// Idempotency PreCheck runs before opening a transaction so cache
		// hits and short-circuit failures (400 / 409 / 422) never touch
		// the writer pool. When idempotency is not configured PreCheck is
		// skipped entirely and behavior is identical to pre-#1123.
		if m.Options.IdempotencyStore != nil {
			result := idempotency.PreCheck(ctx, oc.Headers, oc, m.Options.IdempotencyStore, m.Options.IdempotencyAuth)
			idempotencyOutcomes.WithLabelValues(m.Options.MetricsNamespace, idempotencyOutcomeLabel(result, "")).Inc()
			switch result.Action {
			case idempotency.ActionSkip:
				// no key (or non-mutation) — fall through
			case idempotency.ActionReplay, idempotency.ActionShortCircuit:
				// Both paths short-circuit the request; the outcome
				// (200 cached body, 400/409/422 error) rides in
				// result.Response.Errors[].Extensions so the client
				// gets the same shape via direct subgraph or via the
				// federated gateway (which always returns 200).
				return graphql.OneShot(result.Response)
			case idempotency.ActionProceed:
				ctx = withLease(ctx, result.Lease)
			}
		}
		return m.handleMutationWithTx(ctx, next)
	case ast.Query:
		return m.handleQueryWithReadOnlyTx(ctx, next)
	default:
		return next(ctx)
	}
}

// queryReadOnlyTxOpts is the *sql.TxOptions every GraphQL query uses.
//
// ReadOnly: true is the signal pgMultiDriver.BeginTx (Step 8.1) reads to
// route this tx to the reader pool. REPEATABLE READ guarantees a stable
// snapshot across multiple statements within the request, so federated
// edge resolvers and N+1 follow-ups all observe the same point-in-time
// view without paying for SSI tracking overhead.
var queryReadOnlyTxOpts = &sql.TxOptions{
	ReadOnly:  true,
	Isolation: sql.LevelRepeatableRead,
}

// handleQueryWithReadOnlyTx executes a GraphQL query inside a read-only
// reader-pool tx and returns a response handler that finalizes the tx
// once the (possibly concurrent) edge resolvers complete.
func (m *Middleware[T]) handleQueryWithReadOnlyTx(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
	ns := m.Options.MetricsNamespace
	start := time.Now()

	defer func() {
		txDuration.WithLabelValues(ns).Observe(time.Since(start).Seconds())
		txTotalCounter.WithLabelValues(ns).Inc()
	}()

	tx, err := m.Binder.Begin(ctx, queryReadOnlyTxOpts)
	if err != nil {
		txFailureCounter.WithLabelValues(ns).Inc()
		return graphql.OneShot(graphql.ErrorResponse(ctx, "failed to start read-only transaction: %v", err))
	}

	// Attach tx to context. Queries don't need EnsurePostCommitContainer
	// (no post-commit hooks fire on read-only paths), but resolvers still
	// pull the tx via gqltx.ForContext so the inject step is required.
	queryCtx := m.Binder.Inject(ctx, tx)

	resp := next(queryCtx)

	return m.wrapQueryResponseHandler(ns, queryCtx, resp, tx)
}

// wrapQueryResponseHandler finalizes a read-only query tx once the
// response (and any edge resolvers fired during marshaling) completes.
//
// Read-only txs never conflict at commit time, so retries are unnecessary
// and errors cannot be recovered by re-running the operation. We simply
// commit on success and roll back on failure to release the reader-pool
// connection.
func (m *Middleware[T]) wrapQueryResponseHandler(ns string, ctx context.Context, resp graphql.ResponseHandler, tx Tx) graphql.ResponseHandler {
	return func(callCtx context.Context) *graphql.Response {
		r := resp(ctx)

		if r == nil {
			_ = tx.Rollback()
			txFailureCounter.WithLabelValues(ns).Inc()
			return nil
		}

		if len(r.Errors) != 0 {
			_ = tx.Rollback()
			txFailureCounter.WithLabelValues(ns).Inc()
			return r
		}

		if err := tx.Commit(); err != nil {
			// A commit failure on a read-only tx is unusual but logged
			// at debug rather than promoted to a response error: the
			// resolvers already produced their data and surfacing a
			// commit error to the client would be more confusing than
			// useful. The connection will be reaped by the pool.
			log.ForContext(ctx).Debug().
				Err(err).
				Str("ns", ns).
				Msg("read-only transaction commit failed")
		}

		txSuccessCounter.WithLabelValues(ns).Inc()
		return r
	}
}

// handleMutationWithTx executes the mutation within a transaction and returns a response handler.
func (m *Middleware[T]) handleMutationWithTx(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
	ns := m.Options.MetricsNamespace
	start := time.Now()
	attempts := 0

	defer func() {
		txDuration.WithLabelValues(ns).Observe(time.Since(start).Seconds())
		txTotalCounter.WithLabelValues(ns).Inc()
		if attempts > 0 {
			txRetriesPerTransaction.WithLabelValues(ns).Observe(float64(attempts))
		}
	}()

	maxRetries := db.NumRetriesFromContext(ctx, m.Options.DefaultRetries)

	// Per-attempt context with optional timeout.
	// NOTE: We do NOT use context.WithTimeout here because edge resolvers execute
	// AFTER the mutation completes (during response marshaling), and a timeout
	// during edge resolution would cancel the transaction mid-query causing
	// "driver: bad connection" errors. Instead, we use the base context which
	// doesn't have artificial timeouts, allowing the full GraphQL response to complete.
	attemptCtx := ctx

	// Begin tx. nil opts means "writer pool, default isolation": pgMultiDriver
	// only routes to the reader when opts.ReadOnly is true, and ent's
	// generated *Client.BeginTx forwards nil straight to the underlying
	// driver, so each service's database/sql default isolation applies.
	tx, err := m.Binder.Begin(attemptCtx, nil)
	if err != nil {
		txFailureCounter.WithLabelValues(ns).Inc()
		return graphql.OneShot(graphql.ErrorResponse(ctx, "failed to start transaction: %v", err))
	}

	// Attach tx to context and install a fresh post-commit container (also
	// holds response patches). Must use WithFreshPostCommitContainer, not
	// EnsurePostCommitContainer: on OCC retry the caller's ctx still carries
	// the previous attempt's container, and an "ensure" call would preserve
	// the rolled-back attempt's hooks and patches.
	//
	// Also generate a per-attempt transaction ID. The outbox hook and the
	// workflow-reply middleware read this from ctx to key NATS message IDs
	// and the reply waiter respectively, ensuring deduplication is
	// transaction-scoped (not request-scoped like the OTel trace ID was).
	attemptCtx = m.Binder.Inject(attemptCtx, tx)
	attemptCtx = WithFreshPostCommitContainer(attemptCtx)
	attemptCtx = txid.With(attemptCtx, txid.New())

	// If the operation carries an idempotency lease, insert the in-flight
	// row inside this transaction before executing the mutation. A UNIQUE
	// violation means a concurrent request committed (or is committing)
	// the same key — roll back this attempt and re-route through the
	// PreCheck lookup path so the loser either replays the winner's
	// response (200) or gets 409.
	if lease := leaseFromContext(attemptCtx); lease != nil {
		if short := m.insertInFlight(ns, attemptCtx, tx, lease, &attempts, maxRetries, next); short != nil {
			return short
		}
	}

	// Execute resolver pipeline within this attempt.
	resp := next(attemptCtx)

	// Finalize / retry logic is handled inside the response handler.
	return m.wrapResponseHandler(ns, attemptCtx, resp, tx, &attempts, maxRetries, next)
}

// wrapResponseHandler finalizes the transaction and applies retry rules.
func (m *Middleware[T]) wrapResponseHandler(ns string, ctx context.Context, resp graphql.ResponseHandler, tx Tx, attempts *int, maxRetries int, next graphql.OperationHandler) graphql.ResponseHandler {
	return func(callCtx context.Context) *graphql.Response {
		r := resp(ctx)

		// Defensive: nil response is treated as failure.
		if r == nil {
			log.ForContext(ctx).Error().
				Str("ns", ns).
				Msg("nil response, rolling back transaction")
			_ = tx.Rollback()
			txFailureCounter.WithLabelValues(ns).Inc()

			return nil
		}

		// Error path: handle rollback and retries.
		if len(r.Errors) != 0 {
			log.ForContext(ctx).Error().
				Str("ns", ns).
				Int("error_count", len(r.Errors)).
				Interface("errors", r.Errors).
				Msg("response contains errors, rolling back transaction")
			return m.handleFailure(ns, ctx, tx, attempts, maxRetries, next, callCtx, r)
		}

		// Success path: run hooks then commit.
		log.ForContext(ctx).Debug().
			Str("ns", ns).
			Msg("no errors in response, committing transaction")
		return m.handleSuccess(ns, ctx, tx, r, attempts, maxRetries, next, callCtx)
	}
}

// handleSuccess runs post-commit hooks before committing.
// If hooks fail, it rolls back and returns an error attached to the top-level field.
// maxRetries / next / callCtx feed the OCC retry path for retryable PG
// errors raised by the in-tx idempotency MarkCommitted UPDATE.
func (m *Middleware[T]) handleSuccess(ns string, ctx context.Context, tx Tx, r *graphql.Response, attempts *int, maxRetries int, next graphql.OperationHandler, callCtx context.Context) *graphql.Response {
	// Cache the response inside the mutation transaction. This is the
	// crux of the at-most-once guarantee: the mutation and the cached
	// response either commit together or roll back together. A failure
	// here means we cannot honor the contract on retry, so we roll back
	// rather than committing an un-cacheable mutation.
	if lease := leaseFromContext(ctx); lease != nil {
		if short := m.persistIdempotencyResponse(ns, ctx, tx, r, lease, attempts, maxRetries, next, callCtx); short != nil {
			return short
		}
	}

	if err := tx.Commit(); err != nil {
		txFailureCounter.WithLabelValues(ns).Inc()

		return errorResponseAtTopField(ctx, fmt.Errorf("failed to commit transaction: %w", err))
	}

	if err := RunPostCommit(ctx); err != nil {
		log.ForContext(ctx).Error().
			Err(err).
			Str("ns", ns).
			Str("op", topLevelFieldName(ctx)).
			Msg("post-commit hook failed")
	}

	if err := RunResponsePatches(ctx, r); err != nil {
		log.ForContext(ctx).Error().
			Err(err).
			Str("ns", ns).
			Str("op", topLevelFieldName(ctx)).
			Msg("response patch failed")
	}

	txSuccessCounter.WithLabelValues(ns).Inc()

	return r
}

// handleFailure rolls back and, if errors are retryable, retries the whole operation.
func (m *Middleware[T]) handleFailure(ns string, ctx context.Context, tx Tx, attempts *int, maxRetries int, next graphql.OperationHandler, callCtx context.Context, r *graphql.Response) *graphql.Response {
	_ = tx.Rollback()

	if ErrIsRetryable(r.Errors) {
		*attempts++
		if *attempts > maxRetries {
			txFailureCounter.WithLabelValues(ns).Inc()

			return r
		}

		time.Sleep(db.GetSleepDuration(*attempts))

		// Decrement the budget through ctx: each recursive frame resets its
		// local attempts to 0, so the bound must shrink via ctx or a
		// persistently-retryable error retries forever.
		return m.handleMutationWithTx(db.WithMaxRetries(ctx, maxRetries-1), next)(callCtx)
	}

	txFailureCounter.WithLabelValues(ns).Inc()

	return r
}

// topLevelFieldName extracts the top-level field name of the current operation.
func topLevelFieldName(ctx context.Context) string {
	oc := graphql.GetOperationContext(ctx)
	if oc == nil || oc.Operation == nil {
		return ""
	}

	for _, sel := range oc.Operation.SelectionSet {
		if f, ok := sel.(*ast.Field); ok && f != nil {
			return f.Name
		}
	}

	return ""
}

// errorResponseAtTopField attaches the error to the top-level field if available.
// Falls back to a generic error response otherwise.
func errorResponseAtTopField(ctx context.Context, err error) *graphql.Response {
	field := topLevelFieldName(ctx)
	if field == "" {
		return graphql.ErrorResponse(ctx, "%v", err)
	}

	return &graphql.Response{
		Errors: gqlerror.List{
			&gqlerror.Error{
				Message: err.Error(),
				Path:    ast.Path{ast.PathName(field)},
			},
		},
	}
}
