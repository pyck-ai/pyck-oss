package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/99designs/gqlgen/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pyck-ai/pyck/backend/common/admission"
	"github.com/pyck-ai/pyck/backend/common/gqltx"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
)

// pooledField is a mutation with no exemption and no cap, the neighbour
// every assertion about registerTenant is contrasted with.
const pooledField = "createDataType"

// errProbeConnection is what the probe answers every statement with: these
// tests only ask whether a connection was taken out of the writer pool, not
// what would have been run on it.
var errProbeConnection = errors.New("probe connection: no database here")

// txProbe is a database/sql connector that counts the transactions started
// against it and refuses them. It stands in for the writer pool: the
// question these tests ask of the assembled server is whether a
// registerTenant checks a connection out at all, and a started transaction
// is exactly that.
type txProbe struct{ begins atomic.Int64 }

func (p *txProbe) Connect(context.Context) (driver.Conn, error) { return &txProbeConn{probe: p}, nil }
func (p *txProbe) Driver() driver.Driver                        { return txProbeDriver{} }

type txProbeDriver struct{}

func (txProbeDriver) Open(string) (driver.Conn, error) { return nil, errProbeConnection }

type txProbeConn struct{ probe *txProbe }

func (c *txProbeConn) Prepare(string) (driver.Stmt, error) { return nil, errProbeConnection }
func (c *txProbeConn) Close() error                        { return nil }
func (c *txProbeConn) Begin() (driver.Tx, error) {
	c.probe.begins.Add(1)
	return nil, errProbeConnection
}

// probeClient returns an ent client backed by the probe, so BeginTx is
// observable without a database.
func probeClient(t *testing.T) (*ent.Client, *txProbe) {
	t.Helper()

	probe := &txProbe{}
	db := sql.OpenDB(probe)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	return ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db))), probe
}

// mutationCtx builds the operation context of a single-field mutation,
// which is what both the admission cap and the transaction exemption read
// to find the operation they were configured for.
func mutationCtx(field string) context.Context {
	return graphql.WithOperationContext(context.Background(), &graphql.OperationContext{
		Operation: &ast.OperationDefinition{
			Operation:    ast.Mutation,
			Name:         "Mut",
			SelectionSet: ast.SelectionSet{&ast.Field{Name: field}},
		},
	})
}

// managementExtensions assembles the server's extension list with the probe
// client and returns it together with the two extensions under test.
func managementExtensions(t *testing.T, maxInFlight int) (
	exts []graphql.HandlerExtension, probe *txProbe,
	limiter *admission.Limiter, txMiddleware *gqltx.Middleware[*ent.Tx],
) {
	t.Helper()

	client, probe := probeClient(t)
	exts = graphQLExtensions(graphQLServerConfig{
		RegisterTenantMaxInFlight: maxInFlight,
	}, client, newIdempotencyStore(client))

	for _, ext := range exts {
		switch typed := ext.(type) {
		case *admission.Limiter:
			limiter = typed
		case *gqltx.Middleware[*ent.Tx]:
			txMiddleware = typed
		}
	}
	require.NotNil(t, limiter, "the management server must register the admission limiter")
	require.NotNil(t, txMiddleware, "the management server must register the transaction middleware")

	return exts, probe, limiter, txMiddleware
}

// The two halves of the registerTenant wiring live in the binary, not in
// the packages that implement them: backend/common proves that
// WithoutTransaction skips the transaction and that the limiter refuses,
// but only main decides that registerTenant is the operation both are
// configured for, and in which order the two interceptors run.
//
// Losing the exemption pins one writer connection per in-flight
// registration for the whole registration wait; losing the ordering makes
// every refused registration pay for a connection first. Neither shows up
// as a compile error and neither is visible to any other suite without the
// live stack.
func TestManagementServer_RegisterTenantIsExemptFromTheTransaction(t *testing.T) {
	t.Parallel()

	_, probe, _, txMiddleware := managementExtensions(t, 8)

	ctx := mutationCtx(registerTenantField)
	reached := false
	resp := txMiddleware.InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
		reached = true
		return graphql.OneShot(&graphql.Response{Data: []byte(`{"ok":true}`)})
	})(ctx)

	require.NotNil(t, resp)
	assert.Empty(t, resp.Errors)
	assert.True(t, reached, "the registerTenant resolver must run")
	assert.Zero(t, probe.begins.Load(),
		"registerTenant must open no writer transaction: it commits its own row and then waits, and the wait would hold that connection")
}

// The exemption is for registerTenant alone: a mutation the binary did not
// list must still get its transaction, or the exemption would have turned
// the whole schema non-transactional.
func TestManagementServer_OtherMutationsKeepTheTransaction(t *testing.T) {
	t.Parallel()

	_, probe, _, txMiddleware := managementExtensions(t, 8)

	ctx := mutationCtx(pooledField)
	resp := txMiddleware.InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
		return graphql.OneShot(&graphql.Response{Data: []byte(`{"ok":true}`)})
	})(ctx)

	require.NotNil(t, resp)
	assert.Positive(t, probe.begins.Load(),
		"an unlisted mutation must still check a writer connection out")
	assert.NotEmpty(t, resp.Errors, "the probe refuses the transaction, so the operation fails rather than running without one")
}

// gqlgen runs operation interceptors in registration order, so the cap only
// saves a connection if it is registered first. The assertion is on the
// assembled list, because that order is a property of the binary.
func TestManagementServer_AdmissionRunsBeforeTheTransaction(t *testing.T) {
	t.Parallel()

	exts, _, limiter, txMiddleware := managementExtensions(t, 8)

	admissionIdx, txIdx := -1, -1
	names := make([]string, 0, len(exts))
	for i, ext := range exts {
		names = append(names, ext.ExtensionName())
		switch ext {
		case graphql.HandlerExtension(limiter):
			admissionIdx = i
		case graphql.HandlerExtension(txMiddleware):
			txIdx = i
		}
	}

	require.NotEqual(t, -1, admissionIdx)
	require.NotEqual(t, -1, txIdx)
	assert.Less(t, admissionIdx, txIdx,
		"admission must be registered before the transaction middleware, or a refused registerTenant pays for a database connection first; got %v", names)
}

// The cap the binary configures is the one that refuses: a limiter
// registered for a different field, or with a cap of zero, would admit
// every anonymous registration on the replica.
func TestManagementServer_RegisterTenantIsCapped(t *testing.T) {
	t.Parallel()

	_, probe, limiter, _ := managementExtensions(t, 1)

	next := func(context.Context) graphql.ResponseHandler {
		return graphql.OneShot(&graphql.Response{Data: []byte(`{"ok":true}`)})
	}
	ctx := mutationCtx(registerTenantField)

	// The first execution holds its slot until its response handler runs.
	inFlight := limiter.InterceptOperation(ctx, next)
	refusedCalled := false
	refused := limiter.InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		refusedCalled = true
		return next(c)
	})(ctx)

	require.NotNil(t, refused)
	assert.False(t, refusedCalled, "the refused registration must not reach the resolver")
	require.Len(t, refused.Errors, 1)
	assert.Contains(t, refused.Errors[0].Message, admission.ErrTooManyInFlight.Error())
	assert.Equal(t, ast.Path{ast.PathName(registerTenantField)}, refused.Errors[0].Path,
		"the refusal must name registerTenant, or the federated gateway swallows it")
	assert.Zero(t, probe.begins.Load(), "a refused registration must cost no database connection")

	assert.Empty(t, inFlight(ctx).Errors, "the admitted registration is unaffected by the refusal")
}
