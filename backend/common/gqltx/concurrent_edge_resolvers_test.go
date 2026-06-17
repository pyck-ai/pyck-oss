package gqltx_test

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/gqltx"
)

// sqlTxAdapter adapts a *sql.Tx to the gqltx.Tx interface.
type sqlTxAdapter struct{ tx *sql.Tx }

func (a *sqlTxAdapter) Commit() error   { return a.tx.Commit() }
func (a *sqlTxAdapter) Rollback() error { return a.tx.Rollback() }

// sqlTxClient hands out one *sql.Tx wrapper from a shared *sql.DB.
type sqlTxClient struct{ db *sql.DB }

func (c *sqlTxClient) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sqlTxAdapter, error) {
	tx, err := c.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &sqlTxAdapter{tx: tx}, nil
}

// TestMiddleware_ConcurrentEdgeResolversShareTx is a regression net for the
// original race that motivated Middleware.mu (FINDINGS §3.1).
//
// In production, GraphQL edge resolvers run in parallel within one operation.
// They all observe the same *ent.Tx, which wraps a single *sql.Tx, which
// wraps a single *sql.Conn. Concurrent statements on that connection produce
// "driver: bad connection" or pq driver panics.
//
// This test drives the gqltx Middleware's wrapped ResolverMiddleware from
// N>=8 goroutines, each issuing a real SQL statement against the SAME *sql.Tx.
// It must pass under -race today (global Middleware.mu serializes the conn),
// and continue to pass after Step 2.2 narrows the mutex per-tx (the per-tx
// mutex must still serialize calls touching that same connection).
func TestMiddleware_ConcurrentEdgeResolversShareTx(t *testing.T) {
	t.Parallel()

	// In-memory SQLite (shared cache) is sufficient: database/sql will surface
	// "sql: Transaction has already been committed or rolled back" or
	// "driver: bad connection" if concurrent statements corrupt the *sql.Tx.
	db, err := sql.Open("sqlite3", "file::memory:?cache=shared")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	// Restrict to a single *sql.Conn so all goroutines truly share the
	// underlying connection (mirrors a request's tx semantics).
	db.SetMaxOpenConns(1)

	_, err = db.ExecContext(context.Background(),
		`CREATE TABLE IF NOT EXISTS race_probe (id INTEGER PRIMARY KEY, val INTEGER)`)
	require.NoError(t, err)

	client := &sqlTxClient{db: db}
	mw := gqltx.NewMiddleware[*sqlTxAdapter, *sqlTxClient](client, injectSQLTx, "raceprobe", 0)

	// Begin a tx via the middleware's binder (same path used in handleMutationWithTx).
	m, ok := mw.(*gqltx.Middleware[*sqlTxAdapter])
	require.True(t, ok)

	tx, err := m.Binder.Begin(context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	// Build a mutation OperationContext and let the middleware wrap the
	// ResolverMiddleware (this installs the mutex around every resolver call).
	opCtx := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Mutation},
		ResolverMiddleware: func(ctx context.Context, next graphql.Resolver) (interface{}, error) {
			return next(ctx)
		},
	}

	ctx := graphql.WithOperationContext(context.Background(), opCtx)
	require.Nil(t, m.MutateOperationContext(ctx, opCtx))

	// Spawn N goroutines, each issuing a real SELECT on the SHARED *sql.Tx.
	// Without serialization around tx.QueryContext, database/sql or the
	// underlying driver will return errors or panic when the single conn is
	// used concurrently. With serialization (today: global mutex; after 2.2:
	// per-tx mutex) every goroutine must succeed.
	const goroutines = 16
	const iterations = 25

	var (
		errCount   atomic.Int64
		badConnHit atomic.Int64
		wg         sync.WaitGroup
	)

	wg.Add(goroutines)
	for i := range goroutines {
		go func(workerID int) {
			defer wg.Done()
			for j := range iterations {
				_, rerr := opCtx.ResolverMiddleware(ctx, func(ctx context.Context) (interface{}, error) {
					// Issue a real statement on the shared *sql.Tx.
					var v int
					if qerr := tx.tx.QueryRowContext(ctx, "SELECT 1").Scan(&v); qerr != nil {
						if strings.Contains(qerr.Error(), "bad connection") {
							badConnHit.Add(1)
						}
						return nil, qerr
					}
					if v != 1 {
						return nil, errUnexpectedValue
					}
					return v, nil
				})
				if rerr != nil {
					errCount.Add(1)
					t.Logf("worker %d iter %d: %v", workerID, j, rerr)
				}
			}
		}(i)
	}

	wg.Wait()

	require.EqualValues(t, 0, badConnHit.Load(),
		"got driver: bad connection — gqltx is no longer serializing concurrent resolvers on the same tx")
	require.EqualValues(t, 0, errCount.Load(),
		"resolver errors observed — concurrent statements on the same *sql.Tx corrupted the connection")
}

// injectSQLTx is the InjectTxFunc for sqlTxAdapter. The test never reads the
// tx back from context, but gqltx requires the function to exist.
func injectSQLTx(ctx context.Context, tx *sqlTxAdapter) context.Context {
	return context.WithValue(ctx, sqlTxCtxKey{}, tx)
}

type sqlTxCtxKey struct{}

// errUnexpectedValue is returned when a SELECT 1 doesn't yield 1 (signals
// driver/connection corruption rather than test logic bugs).
var errUnexpectedValue = &probeError{msg: "unexpected scan result on shared tx"}

type probeError struct{ msg string }

func (e *probeError) Error() string { return e.msg }
