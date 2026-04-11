package gqltx

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Tx is the minimal transaction interface (compatible with *ent.Tx).
type Tx interface {
	Commit() error
	Rollback() error
}

// TxClient represents a client that can start a transaction of type T.
type TxClient[T Tx] interface {
	Tx(ctx context.Context) (T, error)
}

// InjectTxFunc injects a transaction into the context (e.g., ent.NewTxContext).
type InjectTxFunc[T Tx] func(ctx context.Context, tx T) context.Context

// NewMiddleware wires a transaction-capable client into the GraphQL server.
func NewMiddleware[T Tx, C TxClient[T]](client C, inject InjectTxFunc[T], namespace string, defaultRetries int) graphql.HandlerExtension {
	return &Middleware[T]{
		Binder: binder[T]{
			Begin:  client.Tx,
			Inject: inject,
		},
		Options: options{
			DefaultRetries:   defaultRetries,
			MetricsNamespace: namespace,
		},
	}
}

// binder declares how to begin a transaction and inject it into a context.
// This keeps the middleware decoupled from any specific ent package.
type binder[T Tx] struct {
	// Begin starts a new transaction.
	Begin func(ctx context.Context) (T, error)
	// Inject returns a context carrying the provided transaction (used by resolvers via ent.TxFromContext).
	Inject func(ctx context.Context, tx T) context.Context
}

// options configures middleware behavior.
type options struct {
	// DefaultRetries is the fallback when no retry count is present in the context.
	DefaultRetries int
	// MetricsNamespace tags Prometheus metrics (use service name).
	MetricsNamespace string
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
)

// Middleware starts one DB transaction per GraphQL mutation,
// retries on retryable DB errors, and orchestrates post-commit hooks.
type Middleware[T Tx] struct {
	Binder  binder[T]
	Options options
	mu      sync.Mutex // Serializes field resolvers during mutations to prevent concurrent transaction access
}

// ExtensionName implements graphql.HandlerExtension.
func (m *Middleware[T]) ExtensionName() string { return "Middleware" }

// Validate implements graphql.HandlerExtension.
func (m *Middleware[T]) Validate(graphql.ExecutableSchema) error { return nil }

// MutateOperationContext implements graphql.OperationContextMutator.
// It serializes field resolvers during mutations to prevent concurrent access to the transaction.
// This is critical because ent.Tx (and sql.Tx) are not safe for concurrent use from multiple goroutines.
func (m *Middleware[T]) MutateOperationContext(ctx context.Context, oc *graphql.OperationContext) *gqlerror.Error {
	if oc.Operation == nil || oc.Operation.Operation != ast.Mutation {
		return nil
	}

	// Wrap the resolver middleware to serialize all field resolvers during mutations.
	// Without this, gqlgen's concurrent field resolution will cause multiple goroutines
	// to access the same transaction simultaneously, leading to "driver: bad connection" errors.
	previous := oc.ResolverMiddleware
	oc.ResolverMiddleware = func(ctx context.Context, next graphql.Resolver) (interface{}, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		return previous(ctx, next)
	}

	return nil
}

// InterceptOperation wraps GraphQL operations; non-mutations bypass transactions.
func (m *Middleware[T]) InterceptOperation(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
	oc := graphql.GetOperationContext(ctx)
	if oc.Operation == nil || oc.Operation.Operation != ast.Mutation {
		return next(ctx)
	}

	return m.handleMutationWithTx(ctx, next)
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

	// Begin tx.
	tx, err := m.Binder.Begin(attemptCtx)
	if err != nil {
		txFailureCounter.WithLabelValues(ns).Inc()
		return graphql.OneShot(graphql.ErrorResponse(ctx, "failed to start transaction: %v", err))
	}

	// Attach tx to context and seed post-commit container (also holds response patches).
	attemptCtx = m.Binder.Inject(attemptCtx, tx)
	attemptCtx = EnsurePostCommitContainer(attemptCtx)

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
		return m.handleSuccess(ns, ctx, tx, r)
	}
}

// handleSuccess runs post-commit hooks before committing.
// If hooks fail, it rolls back and returns an error attached to the top-level field.
func (m *Middleware[T]) handleSuccess(ns string, ctx context.Context, tx Tx, r *graphql.Response) *graphql.Response {
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

		return m.handleMutationWithTx(ctx, next)(callCtx)
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
