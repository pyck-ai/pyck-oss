package resolvers_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	otelapi "go.opentelemetry.io/otel"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/txid"
	"github.com/pyck-ai/pyck/backend/common/validator"
	"github.com/pyck-ai/pyck/backend/common/workflow"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	"github.com/pyck-ai/pyck/backend/workflow/ent/gen/entityeventsoutbox"
	"github.com/pyck-ai/pyck/backend/workflow/ent/gen/enttest"
	"github.com/pyck-ai/pyck/backend/workflow/resolvers"
	"github.com/pyck-ai/pyck/backend/workflow/services"
)

// =============================================================================
// TEST SETUP
// =============================================================================

func TestMain(m *testing.M) {
	cleanup := resolver.MustSetupTestTracer("workflow")
	defer cleanup()
	os.Exit(m.Run())
}

// =============================================================================
// FIXTURES
// =============================================================================

var (
	tenantC = uuid.MustParse("1020ed57-8fca-40e0-958b-10f428774101")

	// Service-specific users with custom user IDs
	userA = &authn.User{
		ID:       uuid.MustParse("9feb0aa8-4011-4b6b-b052-cfd035f4d3e9"),
		TenantID: resolver.TenantA,
		Roles:    map[uuid.UUID]authn.Role{resolver.TenantA: authn.ROLE_ADMIN},
	}
	userNoRole = &authn.User{
		ID:       uuid.MustParse("101049c4-2e95-4246-88b7-e1364b51f101"),
		TenantID: tenantC,
		Roles:    map[uuid.UUID]authn.Role{},
	}

	itemDataTypeID   = uuid.MustParse("94a80c62-3f81-4808-8961-768824d2c325")
	itemDataTypeSlug = "item"
)

// Convenience aliases for tenant IDs
var (
	tenantA = resolver.TenantA
	tenantB = resolver.TenantB
)

// =============================================================================
// TEST ENVIRONMENT
// =============================================================================

type testEnv struct {
	*resolver.TestEnvironment[*ent.Client]
	t                  *testing.T
	MockTemporalClient *mocks.SimpleMockTemporalClient
}

func setup(t *testing.T) *testEnv {
	t.Helper()

	te := &testEnv{
		TestEnvironment: resolver.NewTestEnvironment[*ent.Client](t),
		t:               t,
	}

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	).Debug()

	client.Use(events.MutationEventHook(events.HookConfig{
		Service:        "workflow",
		StreamName:     "pyck",
		EntityFetcher:  events.BuildEntityFetcher(ent.TxFromContext, events.FieldData),
		OutboxInserter: events.NewEntOutboxInserter(ent.TxFromContext),
	}))

	workflowRouter := services.NewSignalRouter(client, services.SignalRouterConfig{
		TemporalURL: "",
	})

	v := validator.NewValidator(te.DataTypeProvider)
	r := resolvers.NewResolver("workflow", client, v, workflowRouter, nil, resolvers.RemoteUIDefaults{})
	schema := resolvers.NewSchema(r)

	te.Init(client, schema, func(s *handler.Server) {
		s.Use(gqltx.NewMiddleware(client, ent.NewTxContext, "workflow-test", 0))
	})

	// Register data type for validation
	jsonSchema, _ := test.LoadSchemaByName("item")
	te.DataTypeProvider.AddDataType(json_schema.DataType{
		ID:         itemDataTypeID,
		Slug:       itemDataTypeSlug,
		TenantID:   tenantA,
		JsonSchema: string(jsonSchema),
	})

	return te
}

func setupWithMockWorkflow(t *testing.T) *testEnv {
	t.Helper()

	te := &testEnv{
		TestEnvironment: resolver.NewTestEnvironment[*ent.Client](t),
		t:               t,
	}

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	).Debug()

	client.Use(events.MutationEventHook(events.HookConfig{
		Service:        "workflow",
		StreamName:     "pyck",
		EntityFetcher:  events.BuildEntityFetcher(ent.TxFromContext, events.FieldData),
		OutboxInserter: events.NewEntOutboxInserter(ent.TxFromContext),
	}))

	// Create mock client factory
	mockFactory := newMockClientFactory()

	workflowRouter := services.NewSignalRouter(client, services.SignalRouterConfig{
		TemporalURL:   "",
		ClientFactory: mockFactory,
	})

	v := validator.NewValidator(te.DataTypeProvider)
	r := resolvers.NewResolver("workflow", client, v, workflowRouter, nil, resolvers.RemoteUIDefaults{})
	schema := resolvers.NewSchema(r)

	te.Init(client, schema, func(s *handler.Server) {
		s.Use(gqltx.NewMiddleware(client, ent.NewTxContext, "workflow-test", 0))
	})

	// Register data type for validation
	jsonSchema, _ := test.LoadSchemaByName("item")
	te.DataTypeProvider.AddDataType(json_schema.DataType{
		ID:         itemDataTypeID,
		Slug:       itemDataTypeSlug,
		TenantID:   tenantA,
		JsonSchema: string(jsonSchema),
	})

	// Expose mock client for test assertions
	te.MockTemporalClient = mockFactory.mockClient

	return te
}

// ctx creates a request context for the given user.
func (te *testEnv) ctx(user *authn.User) context.Context {
	te.t.Helper()
	ctx := request.Context(te.t.Context(), user, user.TenantID)
	tracer := otelapi.Tracer("workflow-test")
	ctx, span := tracer.Start(ctx, "test")
	te.t.Cleanup(func() { span.End() })
	return ctx
}

// =============================================================================
// GRAPHQL EXECUTION HELPERS
// =============================================================================

// execOK executes a GraphQL query and asserts no errors.
func execOK[T any](te *testEnv, ctx context.Context, tpl resolver.Template, args any) T {
	te.t.Helper()
	return resolver.ExecOK[T, *ent.Client](te.TestEnvironment, ctx, tpl, args)
}

// execErr executes a GraphQL query and asserts an error containing the message.
func execErr(te *testEnv, ctx context.Context, tpl resolver.Template, args any, wantErrContains string) {
	te.t.Helper()
	resolver.ExecErr[*ent.Client](te.TestEnvironment, ctx, tpl, args, wantErrContains)
}

// =============================================================================
// EVENT ASSERTIONS
// =============================================================================

// Event represents an expected event.
type Event struct {
	Entity string
	ID     uuid.UUID
	Op     string
}

func Create(entity string, id uuid.UUID) Event { return Event{entity, id, "create"} }
func Update(entity string, id uuid.UUID) Event { return Event{entity, id, "update"} }
func Delete(entity string, id uuid.UUID) Event { return Event{entity, id, "delete"} }

// clearEvents removes all events from the outbox (call after setup, before action).
func (te *testEnv) clearEvents(ctx context.Context) {
	te.t.Helper()
	_, err := te.Ent.EntityEventsOutbox.Delete().Exec(ctx)
	require.NoError(te.t, err)
}

// assertEvents verifies the outbox contains exactly the expected events.
func (te *testEnv) assertEvents(ctx context.Context, expected ...Event) {
	te.t.Helper()

	entries, err := te.Ent.EntityEventsOutbox.Query().
		Order(ent.Asc(entityeventsoutbox.FieldCreatedAt)).
		All(ctx)
	require.NoError(te.t, err)

	if len(expected) == 0 {
		assert.Empty(te.t, entries, "expected no events, got %d", len(entries))
		return
	}

	require.Len(te.t, entries, len(expected), "event count mismatch")

	for i, want := range expected {
		suffix := "." + want.Entity + "." + want.ID.String() + "." + want.Op
		assert.True(te.t, strings.HasSuffix(entries[i].Topic, suffix),
			"event[%d]: expected topic ending with %q, got %q", i, suffix, entries[i].Topic)
	}
}

// assertNoEvents verifies no events were emitted.
func (te *testEnv) assertNoEvents(ctx context.Context) {
	te.t.Helper()
	te.assertEvents(ctx)
}

// assertEventCounts verifies exact event counts per entity schema type.
func (te *testEnv) assertEventCounts(ctx context.Context, expectedCounts map[string]int) {
	te.t.Helper()

	entries, err := te.Ent.EntityEventsOutbox.Query().All(ctx)
	require.NoError(te.t, err)

	actualCounts := make(map[string]int)
	for _, e := range entries {
		parts := strings.Split(e.Topic, ".")
		if len(parts) >= 7 {
			actualCounts[parts[4]]++
		}
	}

	expectedTotal := 0
	for schema, expected := range expectedCounts {
		actual := actualCounts[schema]
		assert.Equal(te.t, expected, actual, "schema %q: expected %d events, got %d", schema, expected, actual)
		expectedTotal += expected
	}

	for schema, count := range actualCounts {
		if _, ok := expectedCounts[schema]; !ok {
			te.t.Errorf("unexpected events for schema %q: got %d", schema, count)
		}
	}

	assert.Len(te.t, entries, expectedTotal, "total event count mismatch")
}

// =============================================================================
// MOCK CLIENT FACTORY
// =============================================================================

// mockClientFactory creates mock workflow clients for testing
type mockClientFactory struct {
	mockClient *mocks.SimpleMockTemporalClient
}

func newMockClientFactory() *mockClientFactory {
	return &mockClientFactory{
		mockClient: mocks.NewSimpleMockTemporalClient(),
	}
}

func (f *mockClientFactory) GetClient(ctx context.Context, namespace string) (*workflow.Client, error) {
	return workflow.NewClient("test", f.mockClient)
}

func (f *mockClientFactory) Close() {
	// No-op for mock
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

func stringPtr(s string) *string {
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}

func intPtr(i int) *int {
	return &i
}

// =============================================================================
// TRANSACTION HELPER
// =============================================================================

func (te *testEnv) withTx(ctx context.Context, fn func(tx *ent.Tx) error) error {
	// Inject a fresh transaction ID so the MutationEventHook (which now
	// requires one for the outbox row's dedup key) is satisfied — this
	// mirrors what the gqltx middleware does at BeginTx in production.
	ctx = txid.With(ctx, txid.New())
	tx, err := te.Ent.Tx(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// =============================================================================
// ENTITY BUILDERS (for testing without validation)
// =============================================================================

type workflowBuilder struct {
	te   *testEnv
	ctx  context.Context //nolint:containedctx // Builder pattern for tests
	user *authn.User
	data map[string]any
	name string
}

func (te *testEnv) newWorkflow(ctx context.Context, user *authn.User) *workflowBuilder {
	return &workflowBuilder{
		te:   te,
		ctx:  ctx,
		user: user,
		name: "wf-" + uuid.New().String()[:8],
	}
}

func (b *workflowBuilder) Data(data map[string]any) *workflowBuilder {
	b.data = data
	return b
}

func (b *workflowBuilder) Name(name string) *workflowBuilder {
	b.name = name
	return b
}

func (b *workflowBuilder) Create() *ent.Workflow {
	b.te.t.Helper()
	var wf *ent.Workflow
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.Workflow.Create().
			SetTenantID(b.user.TenantID).
			SetName(b.name).
			SetTaskQueue("test-queue").
			SetCreatedAt(time.Now()).
			SetCreatedBy(b.user.ID)

		if b.data != nil {
			builder.SetData(b.data)
		}

		var err error
		wf, err = builder.Save(ent.NewTxContext(txid.With(b.ctx, txid.New()), tx))
		return err
	})
	require.NoError(b.te.t, err)
	return wf
}
