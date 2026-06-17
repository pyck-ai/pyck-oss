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
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/txid"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
	"github.com/pyck-ai/pyck/backend/common/validator"

	ent "github.com/pyck-ai/pyck/backend/picking/ent/gen"
	"github.com/pyck-ai/pyck/backend/picking/ent/gen/entityeventsoutbox"
	"github.com/pyck-ai/pyck/backend/picking/ent/gen/enttest"
	"github.com/pyck-ai/pyck/backend/picking/resolvers"
)

// =============================================================================
// TEST SETUP
// =============================================================================

func TestMain(m *testing.M) {
	cleanup := resolver.MustSetupTestTracer("picking")
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

	itemDataTypeID = uuid.MustParse("a5e07166-ea32-4076-b9f1-067fc8be5f02")

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
		Service:        "picking",
		StreamName:     "pyck",
		EntityFetcher:  events.BuildEntityFetcher(ent.TxFromContext, events.FieldData),
		OutboxInserter: events.NewEntOutboxInserter(ent.TxFromContext),
	}))

	v := validator.NewValidator(te.DataTypeProvider)
	r := resolvers.NewResolver("picking", client, v)
	schema := resolvers.NewSchema(r)

	te.Init(client, schema, func(s *handler.Server) {
		s.Use(gqltx.NewMiddleware(client, ent.NewTxContext, "picking-test", 0))
	})

	// Register data type for validation
	jsonSchema, _ := test.LoadSchemaByName("item")
	te.DataTypeProvider.AddDataType(json_schema.DataType{
		ID:         itemDataTypeID,
		Slug:       "item",
		TenantID:   tenantA,
		JsonSchema: string(jsonSchema),
	})

	return te
}

// ctx creates a request context for the given user.
func (te *testEnv) ctx(user *authn.User) context.Context {
	te.t.Helper()
	ctx := request.Context(te.t.Context(), user, user.TenantID)
	tracer := otelapi.Tracer("picking-test")
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

type orderBuilder struct {
	te         *testEnv
	ctx        context.Context //nolint:containedctx // Builder pattern for tests
	user       *authn.User
	customerID uuid.UUID
	data       map[string]any
	deleted    bool
}

func (te *testEnv) newOrder(ctx context.Context, user *authn.User) *orderBuilder {
	return &orderBuilder{
		te:         te,
		ctx:        ctx,
		user:       user,
		customerID: uuidgql.GenerateV7UUID(),
		data:       validData,
	}
}

func (b *orderBuilder) CustomerID(id uuid.UUID) *orderBuilder {
	b.customerID = id
	return b
}

func (b *orderBuilder) Deleted() *orderBuilder {
	b.deleted = true
	return b
}

func (b *orderBuilder) Data(data map[string]any) *orderBuilder {
	b.data = data
	return b
}

func (b *orderBuilder) Create() *ent.Order {
	b.te.t.Helper()
	var order *ent.Order
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.Order.Create().
			SetTenantID(b.user.TenantID).
			SetCustomerID(b.customerID).
			SetData(b.data).
			SetDataTypeID(itemDataTypeID)

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		order, err = builder.Save(ent.NewTxContext(txid.With(b.ctx, txid.New()), tx))
		return err
	})
	require.NoError(b.te.t, err)
	return order
}

type orderItemBuilder struct {
	te       *testEnv
	ctx      context.Context //nolint:containedctx // Builder pattern for tests
	user     *authn.User
	orderID  uuid.UUID
	sku      string
	quantity int64
	data     map[string]any
	deleted  bool
}

func (te *testEnv) newOrderItem(ctx context.Context, user *authn.User, orderID uuid.UUID) *orderItemBuilder {
	return &orderItemBuilder{
		te:       te,
		ctx:      ctx,
		user:     user,
		orderID:  orderID,
		sku:      "SKU-" + uuidgql.GenerateV7UUID().String(),
		quantity: 10,
		data:     validData,
	}
}

func (b *orderItemBuilder) Sku(sku string) *orderItemBuilder {
	b.sku = sku
	return b
}

func (b *orderItemBuilder) Quantity(q int64) *orderItemBuilder {
	b.quantity = q
	return b
}

func (b *orderItemBuilder) Data(data map[string]any) *orderItemBuilder {
	b.data = data
	return b
}

func (b *orderItemBuilder) Deleted() *orderItemBuilder {
	b.deleted = true
	return b
}

func (b *orderItemBuilder) Create() *ent.OrderItems {
	b.te.t.Helper()
	var item *ent.OrderItems
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.OrderItems.Create().
			SetTenantID(b.user.TenantID).
			SetOrderID(b.orderID).
			SetSku(b.sku).
			SetQuantity(b.quantity).
			SetData(b.data).
			SetDataTypeID(itemDataTypeID)

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		item, err = builder.Save(ent.NewTxContext(txid.With(b.ctx, txid.New()), tx))
		return err
	})
	require.NoError(b.te.t, err)
	return item
}

type notificationBuilder struct {
	te      *testEnv
	ctx     context.Context //nolint:containedctx // Builder pattern for tests
	user    *authn.User
	orderID uuid.UUID
	data    map[string]any
	deleted bool
}

func (te *testEnv) newNotification(ctx context.Context, user *authn.User, orderID uuid.UUID) *notificationBuilder {
	return &notificationBuilder{
		te:      te,
		ctx:     ctx,
		user:    user,
		orderID: orderID,
		data:    validData,
	}
}

func (b *notificationBuilder) Data(data map[string]any) *notificationBuilder {
	b.data = data
	return b
}

func (b *notificationBuilder) Deleted() *notificationBuilder {
	b.deleted = true
	return b
}

func (b *notificationBuilder) Create() *ent.OutboundShipmentNotification {
	b.te.t.Helper()
	var notification *ent.OutboundShipmentNotification
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.OutboundShipmentNotification.Create().
			SetTenantID(b.user.TenantID).
			SetOrderID(b.orderID).
			SetData(b.data).
			SetDataTypeID(itemDataTypeID)

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		notification, err = builder.Save(ent.NewTxContext(txid.With(b.ctx, txid.New()), tx))
		return err
	})
	require.NoError(b.te.t, err)
	return notification
}

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
