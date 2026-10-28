package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"entgo.io/contrib/entgql"
	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/riandyrn/otelchi"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/gqlserver"
	"github.com/pyck-ai/pyck/backend/common/liveness"
	livenessmocks "github.com/pyck-ai/pyck/backend/common/liveness/mocks"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/common/workflow"
)

// warmUpTimeout bounds SendQuery's liveness warm-up. The caller's context
// cannot: it may be cancelled on purpose, to test the request.
const warmUpTimeout = 30 * time.Second

type TestRequest struct {
	OperationName string `json:"operationName"`
	Query         string `json:"query"`
}

type EntTestClient interface {
	entgql.TxOpener
	Close() error
}

func NewTestEnvironment[E EntTestClient](t *testing.T) *TestEnvironment[E] {
	t.Helper()

	te := &TestEnvironment[E]{
		T:                t,
		HTTPClient:       http.Client{},
		Publisher:        new(mocks.MockPublisher),
		DataTypeProvider: new(mocks.MockDataTypeProvider),
		TemporalClient:   new(mocks.MockTemporalClient),
	}

	te.WorkflowClient, _ = workflow.NewClient("test", te.TemporalClient)

	return te
}

type TestEnvironment[E EntTestClient] struct {
	initialized bool

	T                *testing.T
	Ent              E
	GQLServer        *handler.Server
	HTTPClient       http.Client
	Publisher        *mocks.MockPublisher
	DataTypeProvider *mocks.MockDataTypeProvider
	WorkflowClient   *workflow.Client
	TemporalClient   *mocks.MockTemporalClient
}

type ServerConfigurator func(s *handler.Server)

type contextKeyNoForcedFeatures struct{}

// WithoutForcedFeatures marks ctx so SendQuery sends only the features ctx
// already carries instead of forcing FEATURE_SYNC_UPDATES onto the
// request. For operations that refuse any X-Pyck-Feature header
// (registerTenant): with the forced feature every such request would be
// refused before it reaches the code under test.
func WithoutForcedFeatures(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKeyNoForcedFeatures{}, true)
}

func (te *TestEnvironment[E]) Init(ent E, gqlSchema graphql.ExecutableSchema, cfgs ...ServerConfigurator) {
	te.T.Helper()

	te.Ent = ent

	te.GQLServer = gqlserver.New(gqlSchema)
	te.GQLServer.Use(entgql.Transactioner{TxOpener: te.Ent})

	for _, c := range cfgs {
		if c != nil {
			c(te.GQLServer)
		}
	}

	te.initialized = true
}

func (te *TestEnvironment[E]) Close(t *testing.T) {
	t.Helper()

	if !te.initialized {
		return
	}

	_ = te.Ent.Close()
}

func (te *TestEnvironment[E]) SendQuery(t *testing.T, ctx context.Context, tpl TemplateRenderer, args any) (func(), *http.Response, error) {
	t.Helper()

	if !te.initialized {
		panic("TestEnvironment not initialized, call Init() first")
	}

	// Force-enable sync update events, so we can actually test them,
	// unless the test sends a mutation that refuses every feature header.
	if noForced, ok := ctx.Value(contextKeyNoForcedFeatures{}).(bool); !ok || !noForced {
		ctx = feature.Context(ctx, feature.FEATURE_SYNC_UPDATES)
	}

	requestGQL := TestRequest{
		Query: tpl.RenderTemplate(args),
	}

	req := request.ForContext(ctx)
	user := req.User()

	// The warm-up keeps ctx's values but not its cancellation: that belongs
	// to the HTTP request, so a cancelled ctx comes back as SendQuery's error.
	// A failed warm-up does too, rather than failing the test here, possibly
	// off the test goroutine.
	warmCtx, cancelWarm := context.WithTimeout(context.WithoutCancel(ctx), warmUpTimeout)
	defer cancelWarm()
	cache, err := livenessmocks.NewWarmTestCache(warmCtx, t, requestTenants(user, req)...)
	if err != nil {
		return func() {}, nil, err
	}

	httpAuth := new(mocks.MockAuthProvider)
	httpAuthMock := httpAuth.On("HTTPMiddleware").Return(mocks.HTTPMiddleware(&user)).Once()
	defer httpAuthMock.Parent.AssertExpectations(t)

	httpRouter := chi.NewRouter()
	httpRouter.Use(
		otelchi.Middleware("test-resolver"),
		httpAuth.HTTPMiddleware(),
		tenantMiddleware(t, cache),
		feature.HTTPMiddleware(),
	)
	httpRouter.Handle("/query", te.GQLServer)

	httpServer := httptest.NewServer(httpRouter)
	defer httpServer.Close()

	payload, err := json.Marshal(requestGQL)
	require.NoError(t, err, "failed to marshal request GQL")
	t.Log("\nHTTP Request:\t ", reindent(fmt.Sprint(requestGQL), "\t\t\t"))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/query", bytes.NewBuffer(payload))

	for _, tenantID := range req.TenantIDs() {
		if tenantID == uuid.Nil {
			continue
		}
		t.Log("Adding tenant ID header:", tenantID.String())
		httpReq.Header.Add(tenant.TenantIDHeader, tenantID.String())
	}

	for _, feat := range feature.ForContext(ctx) {
		t.Log("Adding feature header:", feat.String())
		httpReq.Header.Add(feature.FeatureHeader, feat.String())
	}

	if err != nil {
		require.NoError(t, err, "failed to create new request")
	}

	httpReq.Header.Set("Content-Type", "application/json; charset=UTF-8")

	resp, err := te.HTTPClient.Do(httpReq)

	return func() {
		if resp != nil {
			_ = resp.Body.Close()
		}
	}, resp, err
}

// requestTenants is every tenant the tenant middleware may check for a
// request sent as user with req's selection: the home tenant, the tenants
// named in the request and every tenant the user holds a role in, which
// covers an "all" selection. Nil is skipped, as the header loop skips it.
func requestTenants(user authn.User, req request.RequestContext) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{})
	var ids []uuid.UUID
	add := func(id uuid.UUID) {
		if _, dup := seen[id]; dup || id == uuid.Nil {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	add(user.TenantID)
	for _, id := range req.TenantIDs() {
		add(id)
	}
	for _, id := range user.TenantIDs() {
		add(id)
	}
	return ids
}

func (te *TestEnvironment[E]) ReadResponse(t *testing.T, resp *http.Response, result any) error {
	t.Helper()

	if !te.initialized {
		panic("TestEnvironment not initialized, call Init() first")
	}

	body, _ := io.ReadAll(resp.Body)
	err := json.Unmarshal(body, &result)
	require.NoError(t, err, "failed to unmarshal response body")

	resultJSON, err := json.MarshalIndent(result, "", "\t")
	require.NoError(t, err, "failed to marshal response JSON")
	t.Log("\n\tHTTP Response:\t", reindent(string(resultJSON), "\t\t\t"))

	return err
}

// TenantMiddleware returns the tenant middleware a test router mounts: a
// test liveness cache over a source that reports every tenant live. The
// cache starts cold; a router serving one fixed user mounts
// TenantMiddlewareFor instead. A construction error fails the test.
func TenantMiddleware(tb testing.TB) func(http.Handler) http.Handler {
	tb.Helper()
	return tenantMiddleware(tb, livenessmocks.NewTestCache(tb))
}

// TenantMiddlewareFor is TenantMiddleware warmed for every tenant a request
// of user may check (see requestTenants and
// livenessmocks.NewWarmTestCache), so a router serving that fixed user never
// waits on a lookup. A construction error fails the test.
func TenantMiddlewareFor(tb testing.TB, user authn.User) func(http.Handler) http.Handler {
	tb.Helper()
	ids := requestTenants(user, request.RequestContext{})
	cache, err := livenessmocks.NewWarmTestCache(tb.Context(), tb, ids...)
	require.NoError(tb, err)
	return tenantMiddleware(tb, cache)
}

// tenantMiddleware is TenantMiddleware over the given liveness cache.
func tenantMiddleware(tb testing.TB, cache *liveness.Cache) func(http.Handler) http.Handler {
	tb.Helper()
	mw, err := tenant.HTTPMiddleware(cache, livenessmocks.NoDisabledTenants)
	require.NoError(tb, err, "tenant middleware")
	return mw
}
