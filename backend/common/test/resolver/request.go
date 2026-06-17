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

	"entgo.io/contrib/entgql"
	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/riandyrn/otelchi"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/common/workflow"
)

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

func (te *TestEnvironment[E]) Init(ent E, gqlSchema graphql.ExecutableSchema, cfgs ...ServerConfigurator) {
	te.T.Helper()

	te.Ent = ent

	te.GQLServer = handler.NewDefaultServer(gqlSchema)
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

	// Force-enable sync update events, so we can actually test them
	ctx = feature.Context(ctx, feature.FEATURE_SYNC_UPDATES)

	requestGQL := TestRequest{
		Query: tpl.RenderTemplate(args),
	}

	req := request.ForContext(ctx)
	user := req.User()

	httpAuth := new(mocks.MockAuthProvider)
	httpAuthMock := httpAuth.On("HTTPMiddleware").Return(mocks.HTTPMiddleware(&user)).Once()
	defer httpAuthMock.Parent.AssertExpectations(t)

	httpRouter := chi.NewRouter()
	httpRouter.Use(
		otelchi.Middleware("test-resolver"),
		httpAuth.HTTPMiddleware(),
		tenant.HTTPMiddleware(),
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
