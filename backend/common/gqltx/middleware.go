package gqltx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
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

// TransactionID returns the per-transaction UUID this middleware generates at
// BeginTx (regenerated on every OCC retry, so it always identifies the
// committing attempt — see handleMutationWithTx). Mutation resolvers assign
// it to their payload's transactionID field — the handle clients use to look
// up the workflows the mutation started via
// workflowExecutions(where: {transactionID: ...}). The same ID is stamped on
// the transaction's outbox rows and carried into Temporal as the
// pyck_transaction_id search attribute.
//
// Assigning it inside the resolver means the handle is part of the response
// before the idempotency middleware caches the body, so idempotent replays
// return the identical handle.
//
// Returns uuid.Nil when no transaction is active on ctx (gqltx did not run).
// On the non-null ID! payload fields this serializes to null and fails the
// request loudly instead of shipping a bogus handle.
func TransactionID(ctx context.Context) uuid.UUID {
	id, _ := txid.FromContext(ctx)
	return id
}

// EventCount returns how many outbox events the transaction on ctx has
// written so far (0 outside a transaction, or when FEATURE_SUPPRESS_EVENTS
// suppressed them). Mutation payloads expose it as eventCount: Int!, next to
// the transactionID handle, so a client knows whether the transaction has
// anything to route. Unlike transactionID, resolvers do not assign it: the
// count is only final once the resolver has written its events, so the
// middleware fills the field itself when gqlgen resolves it (InterceptField).
func EventCount(ctx context.Context) int {
	return txid.EventCount(ctx)
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
	// NoTxOperations names the top-level mutation fields that run without
	// the outer writer transaction; see WithoutTransaction. Nil (the
	// default) means every mutation gets one.
	NoTxOperations map[string]struct{}
}

// ErrMixedTransactionExemption refuses a mutation document that selects a
// transaction-exempt field alongside another field. The two halves cannot
// share one answer: the exempt resolver must not be wrapped in a writer
// transaction, and the others must not run without one.
var ErrMixedTransactionExemption = errors.New(
	"a mutation that runs without a transaction must be the only field of its operation")

// WithoutTransaction exempts the named top-level mutation fields from the
// outer writer transaction: they run with a fresh post-commit container and
// a transaction ID, but no connection is checked out of the writer pool for
// the duration of the request, and neither the OCC retry loop nor the
// Idempotency-Key lease applies.
//
// This is for the rare mutation that must commit its own work before it can
// observe the effect of that commit: a resolver that waits for a consumer
// of its outbox event cannot hold the transaction that has to commit first,
// and would otherwise pin a writer connection for the whole wait. Such a
// resolver owns its transactions (see the management registerTenant
// resolver) and is therefore not replay-safe: listing an operation here
// means its side effects are not rolled back by an error later in the
// response, and an Idempotency-Key on it is ignored rather than honoured.
//
// Names are top-level GraphQL field names, not operation names. The
// exemption is for the only field of its operation: a document that selects
// an exempt field together with any other field (a repeat of the same
// field under an alias included, which is n executions of it in one request)
// is refused with ErrMixedTransactionExemption rather than served either
// way.
func WithoutTransaction(ops ...string) Option {
	return func(o *options) {
		if o.NoTxOperations == nil {
			o.NoTxOperations = make(map[string]struct{}, len(ops))
		}
		for _, op := range ops {
			o.NoTxOperations[op] = struct{}{}
		}
	}
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

// eventCountField is the GraphQL field name of the per-mutation event tally.
const eventCountField = "eventCount"

// InterceptField implements graphql.FieldInterceptor. It answers the
// `eventCount: Int!` field of every mutation payload with the transaction's
// outbox-event tally (EventCount).
//
// gqlgen resolves a payload's fields only after the mutation's root resolver
// returned, so every outbox row the mutation wrote (by the ent hook or
// directly) is already counted, and the transaction has not committed yet.
// Doing it here keeps the ~70 mutation resolvers free of bookkeeping they
// could place too early or forget; a field that isn't asked for costs nothing.
func (m *Middleware[T]) InterceptField(ctx context.Context, next graphql.Resolver) (any, error) {
	oc := graphql.GetOperationContext(ctx)
	fc := graphql.GetFieldContext(ctx)
	if oc == nil || oc.Operation == nil || oc.Operation.Operation != ast.Mutation ||
		fc == nil || fc.Field.Field == nil || fc.Field.Name != eventCountField {
		return next(ctx)
	}
	return EventCount(ctx), nil
}

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
		// The exemption is checked before the idempotency PreCheck: an
		// exempt mutation commits outside this middleware's control, so a
		// lease taken here could neither roll back with it nor cache a
		// response that matches what was committed.
		exempt, mixed := m.exemptFromTx(TopLevelFieldNames(ctx))
		if mixed {
			return graphql.OneShot(ErrorResponseAtTopField(ctx, ErrMixedTransactionExemption))
		}
		if exempt {
			return m.handleMutationWithoutTx(ctx, next)
		}
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

// exemptFromTx decides whether a mutation runs without the outer writer
// transaction, from the full set of top-level fields it selects.
//
// The exemption is for a single field: a document that names an exempt
// field alongside any other field, or that names the same exempt field
// more than once, is refused (mixed), because the alternatives are
// both wrong. Opening a transaction around the exempt resolver hands the
// caller the connection hold the exemption exists to prevent: the field
// list is caller-controlled, so that would be an opt-out. Skipping the
// transaction for the other fields runs resolvers that expect one without
// it. Refusing is the only answer that keeps the property under a document
// nobody on this side wrote.
//
// A repeat of the exempt field is the same problem wearing the caller's
// aliases: `mutation { a: f(...) b: f(...) }` selects one field name twice,
// and serving it would run the exempt resolver once per alias within one
// operation: N times the wait the exemption exists for, behind a single
// pass of whatever bounds that operation per request.
func (m *Middleware[T]) exemptFromTx(fields []string) (exempt, mixed bool) {
	if len(m.Options.NoTxOperations) == 0 || len(fields) == 0 {
		return false, false
	}

	matched := 0
	for _, field := range fields {
		if _, ok := m.Options.NoTxOperations[field]; ok {
			matched++
		}
	}

	switch matched {
	case 0:
		return false, false
	case len(fields):
		// All the fields are exempt: one of them is the exemption, more
		// than one is the same field repeated under aliases.
		return len(fields) == 1, len(fields) > 1
	default:
		return false, true
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

// handleMutationWithoutTx executes a WithoutTransaction mutation with no
// writer transaction of its own, so no pooled connection is held for the
// life of the request. The resolver is responsible for its own
// transactions; the post-commit hooks still run, keyed by a fresh container
// and a fresh transaction ID, because resolvers and the outbox hook read
// both from the context unconditionally.
//
// Errors in the response are returned as they are: there is nothing to roll
// back and nothing to retry, and re-running a resolver that already
// committed would repeat its side effects. The transaction metrics stay
// untouched for the same reason: no transaction was opened.
//
// The hooks run whether or not the response carries errors, which is the
// opposite of the transactional path: there the rollback undid the write a
// hook would have announced, here the resolver's own commits stand, so
// skipping the hook would drop a notification about work that is durable.
func (m *Middleware[T]) handleMutationWithoutTx(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
	ns := m.Options.MetricsNamespace

	opCtx := WithFreshPostCommitContainer(ctx)
	opCtx = txid.With(opCtx, txid.New())

	resp := next(opCtx)

	return func(callCtx context.Context) *graphql.Response {
		r := resp(opCtx)
		if r == nil {
			return r
		}

		if err := RunPostCommit(opCtx); err != nil {
			log.ForContext(opCtx).Error().
				Err(err).
				Str("ns", ns).
				Str("op", TopLevelFieldName(opCtx)).
				Msg("post-commit hook failed")
		}

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

	// Attach tx to context and install a fresh post-commit container. Must
	// use WithFreshPostCommitContainer, not EnsurePostCommitContainer: on OCC
	// retry the caller's ctx still carries the previous attempt's container,
	// and an "ensure" call would preserve the rolled-back attempt's hooks.
	//
	// Also generate a per-attempt transaction ID. The outbox hook reads it
	// from ctx to key NATS message IDs (transaction-scoped dedup, not
	// request-scoped like the OTel trace ID was), and mutation resolvers
	// return it as the client-facing handle via TransactionID(ctx).
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

		return ErrorResponseAtTopField(ctx, fmt.Errorf("failed to commit transaction: %w", err))
	}

	if err := RunPostCommit(ctx); err != nil {
		log.ForContext(ctx).Error().
			Err(err).
			Str("ns", ns).
			Str("op", TopLevelFieldName(ctx)).
			Msg("post-commit hook failed")
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

// TopLevelFieldName extracts the first top-level field name of the current
// operation, or "" when ctx carries no operation or no field. Exported
// because every extension that refuses an operation before the resolvers
// run has to address the error to a field the client asked for (see
// ErrorResponseAtTopField).
//
// Naming a field is all this answer is good for. It is NOT a key to gate
// an operation on: the client decides which field comes first, so a gate
// keyed on it is bypassed by naming another field before the gated one.
// Use TopLevelFieldNames and decide over the whole set.
func TopLevelFieldName(ctx context.Context) string {
	names := TopLevelFieldNames(ctx)
	if len(names) == 0 {
		return ""
	}

	return names[0]
}

// TopLevelFieldNames returns every top-level field the current operation
// selects, in document order, flattening inline fragments and fragment
// spreads; nil when ctx carries no operation.
//
// This is the set a gate decides over. The walk is
// idempotency.TopLevelFieldNames, shared so that the single-mutation rule,
// the transaction exemption and the admission cap cannot disagree about
// what a document asks for.
func TopLevelFieldNames(ctx context.Context) []string {
	oc := graphql.GetOperationContext(ctx)
	if oc == nil || oc.Operation == nil {
		return nil
	}

	return idempotency.TopLevelFieldNames(oc.Operation.SelectionSet)
}

// ErrorResponseAtTopField attaches the error to the top-level field if
// available and falls back to a generic error response otherwise. The path
// matters: the federated gateway only surfaces a subgraph error to the
// client when it names the field the client asked for. The response is
// built here, not by gqlgen, so the error presenter never sees it; a
// constraint violation is hidden explicitly, or a deferred constraint
// failing at commit would leak its table and constraint names.
func ErrorResponseAtTopField(ctx context.Context, err error) *graphql.Response {
	message, hidden := db.HideConstraintViolation(ctx, err)
	if !hidden {
		message = err.Error()
	}

	field := TopLevelFieldName(ctx)
	if field == "" {
		return graphql.ErrorResponse(ctx, "%s", message)
	}

	return &graphql.Response{
		Errors: gqlerror.List{
			&gqlerror.Error{
				Message: message,
				Path:    ast.Path{ast.PathName(field)},
			},
		},
	}
}
