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

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
	"github.com/pyck-ai/pyck/backend/common/validator"

	ent "github.com/pyck-ai/pyck/backend/main-data/ent/gen"
	"github.com/pyck-ai/pyck/backend/main-data/ent/gen/entityeventsoutbox"
	"github.com/pyck-ai/pyck/backend/main-data/ent/gen/enttest"
	"github.com/pyck-ai/pyck/backend/main-data/resolvers"
)

// =============================================================================
// TEST SETUP
// =============================================================================

func TestMain(m *testing.M) {
	cleanup := resolver.MustSetupTestTracer("main-data")
	defer cleanup()
	os.Exit(m.Run())
}

// =============================================================================
// FIXTURES
// =============================================================================

var (
	tenantA = resolver.TenantA
	tenantB = resolver.TenantB

	userA = &authn.User{
		ID:       uuid.MustParse("9feb0aa8-4011-4b6b-b052-cfd035f4d3e9"),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_ADMIN},
	}
	userB = &authn.User{
		ID:       uuid.MustParse("509e889e-6983-491f-86b1-267174288fef"),
		TenantID: tenantB,
		Roles:    map[uuid.UUID]authn.Role{tenantB: authn.ROLE_ADMIN},
	}

	dataTypeIDTenantA  = uuid.MustParse("94a80c62-3f81-4808-8961-768824d2c325")
	dataTypeIDTenantA2 = uuid.MustParse("14a80c62-3f81-4808-8961-768824d2c325")
	dataTypeIDTenantB  = uuid.MustParse("0193547d-d23d-73d1-8300-e9ddf1a26b8d")
	dataTypeIDTenantB2 = uuid.MustParse("0193547d-d23d-75b0-89c8-f4edb74e7f87")

	validData = map[string]any{
		"type": "custom",
		"sum":  float64(15),
		"meta": map[string]any{
			"name":   "TestItem",
			"weight": float64(50),
			"tags":   []any{"foo", "bar"},
		},
	}
)

// =============================================================================
// TEST ENVIRONMENT
// =============================================================================

type testEnv struct {
	*resolver.TestEnvironment[*ent.Client]
	t *testing.T
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
		Service:        "main-data",
		StreamName:     "pyck",
		EntityFetcher:  events.BuildEntityFetcher(ent.TxFromContext, events.FieldData),
		OutboxInserter: events.NewEntOutboxInserter(ent.TxFromContext),
	}))

	v := validator.NewValidator(te.DataTypeProvider)
	r := resolvers.NewResolver("main-data", client, v)
	schema := resolvers.NewSchema(r)

	te.Init(client, schema, func(s *handler.Server) {
		s.Use(gqltx.NewMiddleware(client, ent.NewTxContext, "main-data-test", 0))
	})

	// Register data types for validation
	te.loadDataTypes()

	return te
}

func (te *testEnv) loadDataTypes() {
	schema, _ := test.LoadSchemaByName("item")
	schema2, _ := test.LoadSchemaByName("item_unique_name")
	te.DataTypeProvider.AddDataType([]json_schema.DataType{
		{
			ID:         dataTypeIDTenantA,
			Slug:       "item",
			TenantID:   tenantA,
			JsonSchema: string(schema),
		},
		{
			ID:         dataTypeIDTenantA2,
			Slug:       "item_unique_name",
			TenantID:   tenantA,
			JsonSchema: string(schema2),
		},
		{
			ID:         dataTypeIDTenantB,
			Slug:       "item",
			TenantID:   tenantB,
			JsonSchema: string(schema),
		},
		{
			ID:         dataTypeIDTenantB2,
			Slug:       "item_unique_name",
			TenantID:   tenantB,
			JsonSchema: string(schema2),
		},
	}...)
}

// ctx creates a request context for the given user.
func (te *testEnv) ctx(user *authn.User) context.Context {
	te.t.Helper()
	ctx := request.Context(te.t.Context(), user, user.TenantID)
	tracer := otelapi.Tracer("main-data-test")
	ctx, span := tracer.Start(ctx, "test")
	te.t.Cleanup(func() { span.End() })
	return ctx
}

// ctxWithDeleted creates a context that includes soft-deleted records.
func (te *testEnv) ctxWithDeleted(user *authn.User) context.Context {
	return feature.Context(te.ctx(user), feature.FEATURE_SHOW_DELETED)
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
// ENTITY BUILDERS
// =============================================================================

type customerBuilder struct {
	te         *testEnv
	ctx        context.Context //nolint:containedctx // Builder pattern for tests
	user       *authn.User
	data       map[string]any
	dataTypeID uuid.UUID
	deleted    bool
}

func (te *testEnv) newCustomer(ctx context.Context, user *authn.User) *customerBuilder {
	return &customerBuilder{
		te:         te,
		ctx:        ctx,
		user:       user,
		data:       validData,
		dataTypeID: dataTypeIDTenantA,
	}
}

func (b *customerBuilder) Data(data map[string]any) *customerBuilder {
	b.data = data
	return b
}

func (b *customerBuilder) DataTypeID(id uuid.UUID) *customerBuilder {
	b.dataTypeID = id
	return b
}

func (b *customerBuilder) Deleted() *customerBuilder {
	b.deleted = true
	return b
}

func (b *customerBuilder) Create() *ent.Customer {
	b.te.t.Helper()
	var customer *ent.Customer
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.Customer.Create().
			SetTenantID(b.user.TenantID).
			SetData(b.data).
			SetDataTypeID(b.dataTypeID)

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		customer, err = builder.Save(ent.NewTxContext(b.ctx, tx))
		return err
	})
	require.NoError(b.te.t, err)
	return customer
}

type supplierBuilder struct {
	te         *testEnv
	ctx        context.Context //nolint:containedctx // Builder pattern for tests
	user       *authn.User
	data       map[string]any
	dataTypeID uuid.UUID
	deleted    bool
}

func (te *testEnv) newSupplier(ctx context.Context, user *authn.User) *supplierBuilder {
	return &supplierBuilder{
		te:         te,
		ctx:        ctx,
		user:       user,
		data:       validData,
		dataTypeID: dataTypeIDTenantA,
	}
}

func (b *supplierBuilder) Data(data map[string]any) *supplierBuilder {
	b.data = data
	return b
}

func (b *supplierBuilder) DataTypeID(id uuid.UUID) *supplierBuilder {
	b.dataTypeID = id
	return b
}

func (b *supplierBuilder) Deleted() *supplierBuilder {
	b.deleted = true
	return b
}

func (b *supplierBuilder) Create() *ent.Supplier {
	b.te.t.Helper()
	var supplier *ent.Supplier
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.Supplier.Create().
			SetTenantID(b.user.TenantID).
			SetData(b.data).
			SetDataTypeID(b.dataTypeID)

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		supplier, err = builder.Save(ent.NewTxContext(b.ctx, tx))
		return err
	})
	require.NoError(b.te.t, err)
	return supplier
}

func (te *testEnv) withTx(ctx context.Context, fn func(tx *ent.Tx) error) error {
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
// ENTITY BUILDERS WITHOUT DATA TYPE (for testing without validation)
// =============================================================================

type customerBuilderNoDataType struct {
	te      *testEnv
	ctx     context.Context //nolint:containedctx // Builder pattern for tests
	user    *authn.User
	data    map[string]any
	deleted bool
}

func (te *testEnv) newCustomerNoDataType(ctx context.Context, user *authn.User) *customerBuilderNoDataType {
	return &customerBuilderNoDataType{
		te:   te,
		ctx:  ctx,
		user: user,
		data: validData,
	}
}

func (b *customerBuilderNoDataType) Data(data map[string]any) *customerBuilderNoDataType {
	b.data = data
	return b
}

func (b *customerBuilderNoDataType) NoData() *customerBuilderNoDataType {
	b.data = nil
	return b
}

func (b *customerBuilderNoDataType) Deleted() *customerBuilderNoDataType {
	b.deleted = true
	return b
}

func (b *customerBuilderNoDataType) Create() *ent.Customer {
	b.te.t.Helper()
	var customer *ent.Customer
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.Customer.Create().
			SetTenantID(b.user.TenantID)

		if b.data != nil {
			builder.SetData(b.data)
		}

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		customer, err = builder.Save(ent.NewTxContext(b.ctx, tx))
		return err
	})
	require.NoError(b.te.t, err)
	return customer
}

type supplierBuilderNoDataType struct {
	te      *testEnv
	ctx     context.Context //nolint:containedctx // Builder pattern for tests
	user    *authn.User
	data    map[string]any
	deleted bool
}

func (te *testEnv) newSupplierNoDataType(ctx context.Context, user *authn.User) *supplierBuilderNoDataType {
	return &supplierBuilderNoDataType{
		te:   te,
		ctx:  ctx,
		user: user,
		data: validData,
	}
}

func (b *supplierBuilderNoDataType) Data(data map[string]any) *supplierBuilderNoDataType {
	b.data = data
	return b
}

func (b *supplierBuilderNoDataType) NoData() *supplierBuilderNoDataType {
	b.data = nil
	return b
}

func (b *supplierBuilderNoDataType) Deleted() *supplierBuilderNoDataType {
	b.deleted = true
	return b
}

func (b *supplierBuilderNoDataType) Create() *ent.Supplier {
	b.te.t.Helper()
	var supplier *ent.Supplier
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.Supplier.Create().
			SetTenantID(b.user.TenantID)

		if b.data != nil {
			builder.SetData(b.data)
		}

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		supplier, err = builder.Save(ent.NewTxContext(b.ctx, tx))
		return err
	})
	require.NoError(b.te.t, err)
	return supplier
}

// =============================================================================
// ID GENERATOR
// =============================================================================

func newID() uuid.UUID {
	return uuidgql.GenerateV7UUID()
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
		// Topic format: pyck.<tenantID>.crud.<service>.<entity>.<id>.<op>
		// Entity name in topic is lowercase
		suffix := "." + strings.ToLower(want.Entity) + "." + want.ID.String() + "." + want.Op
		assert.True(te.t, strings.HasSuffix(entries[i].Topic, suffix),
			"event[%d]: expected topic ending with %q, got %q", i, suffix, entries[i].Topic)
	}
}

// assertNoEvents verifies no events were emitted.
func (te *testEnv) assertNoEvents(ctx context.Context) {
	te.t.Helper()
	te.assertEvents(ctx)
}
