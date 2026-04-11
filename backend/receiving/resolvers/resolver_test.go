package resolvers_test

import (
	"context"
	"encoding/json"
	"net/http"
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
	"github.com/pyck-ai/pyck/backend/common/otel"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
	"github.com/pyck-ai/pyck/backend/common/validator"

	ent "github.com/pyck-ai/pyck/backend/receiving/ent/gen"
	"github.com/pyck-ai/pyck/backend/receiving/ent/gen/entityeventsoutbox"
	"github.com/pyck-ai/pyck/backend/receiving/ent/gen/enttest"
	"github.com/pyck-ai/pyck/backend/receiving/resolvers"
)

// =============================================================================
// TEST SETUP
// =============================================================================

func TestMain(m *testing.M) {
	tracer, err := otel.SetupTracer("receiving-test", "test", &otel.OTelConfig{})
	if err != nil {
		panic("failed to setup test tracer: " + err.Error())
	}
	defer tracer.Close()
	os.Exit(m.Run())
}

// =============================================================================
// FIXTURES
// =============================================================================

var (
	tenantA = uuid.MustParse("b98b88eb-ce77-4e9a-a224-d37443a9c5c1")
	tenantB = uuid.MustParse("9820ed57-8fca-40e0-958b-f4f428774cde")

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

	itemDataTypeID = uuid.MustParse("94a80c62-3f81-4808-8961-768824d2c325")

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
		Service:        "receiving",
		StreamName:     "pyck",
		EntityFetcher:  events.BuildEntityFetcher(ent.TxFromContext, events.FieldData),
		OutboxInserter: events.NewEntOutboxInserter(ent.TxFromContext),
	}))

	v := validator.NewValidator(te.DataTypeProvider)
	r := resolvers.NewResolver("receiving", client, v)
	schema := resolvers.NewSchema(r)

	te.Init(client, schema, func(s *handler.Server) {
		s.Use(gqltx.NewMiddleware(client, ent.NewTxContext, "receiving-test", 0))
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
	tracer := otelapi.Tracer("receiving-test")
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

type gqlResult[T any] struct {
	Data   T
	Errors []gqlError
}

type gqlError struct {
	Message string
	Path    []string
}

// exec executes a GraphQL query and returns the parsed response.
func exec[T any](te *testEnv, ctx context.Context, tpl resolver.Template, args any) gqlResult[T] {
	te.t.Helper()

	closeResp, resp, err := te.SendQuery(te.t, ctx, tpl, args)
	defer closeResp()
	require.NoError(te.t, err)
	require.Equal(te.t, http.StatusOK, resp.StatusCode)

	var result gqlResult[T]
	require.NoError(te.t, te.ReadResponse(te.t, resp, &result))
	return result
}

// execOK executes a GraphQL query and asserts no errors.
func execOK[T any](te *testEnv, ctx context.Context, tpl resolver.Template, args any) T {
	te.t.Helper()
	result := exec[T](te, ctx, tpl, args)
	require.Empty(te.t, result.Errors, "unexpected GraphQL errors: %v", result.Errors)
	return result.Data
}

// execErr executes a GraphQL query and asserts an error containing the message.
func execErr(te *testEnv, ctx context.Context, tpl resolver.Template, args any, wantErrContains string) {
	te.t.Helper()
	var result gqlResult[json.RawMessage]
	closeResp, resp, err := te.SendQuery(te.t, ctx, tpl, args)
	defer closeResp()
	require.NoError(te.t, err)
	require.NoError(te.t, te.ReadResponse(te.t, resp, &result))
	require.NotEmpty(te.t, result.Errors, "expected error containing %q", wantErrContains)
	assert.Contains(te.t, result.Errors[0].Message, wantErrContains)
}

// =============================================================================
// ENTITY BUILDERS
// =============================================================================

type inboundBuilder struct {
	te      *testEnv
	ctx     context.Context //nolint:containedctx // Builder pattern for tests
	user    *authn.User
	orderID string
	data    map[string]any
	deleted bool
}

func (te *testEnv) newInbound(ctx context.Context, user *authn.User) *inboundBuilder {
	return &inboundBuilder{
		te:      te,
		ctx:     ctx,
		user:    user,
		orderID: uuidgql.GenerateV7UUID().String(),
		data:    validData,
	}
}

func (b *inboundBuilder) OrderID(id string) *inboundBuilder {
	b.orderID = id
	return b
}

func (b *inboundBuilder) Data(data map[string]any) *inboundBuilder {
	b.data = data
	return b
}

func (b *inboundBuilder) Deleted() *inboundBuilder {
	b.deleted = true
	return b
}

func (b *inboundBuilder) Create() *ent.Inbound {
	b.te.t.Helper()
	var inbound *ent.Inbound
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.Inbound.Create().
			SetTenantID(b.user.TenantID).
			SetOrderID(b.orderID).
			SetData(b.data).
			SetDataTypeID(itemDataTypeID)

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		inbound, err = builder.Save(ent.NewTxContext(b.ctx, tx))
		return err
	})
	require.NoError(b.te.t, err)
	return inbound
}

type itemBuilder struct {
	te        *testEnv
	ctx       context.Context //nolint:containedctx // Builder pattern for tests
	user      *authn.User
	inboundID uuid.UUID
	sku       string
	quantity  int64
	data      map[string]any
	deleted   bool
}

func (te *testEnv) newItem(ctx context.Context, user *authn.User, inboundID uuid.UUID) *itemBuilder {
	return &itemBuilder{
		te:        te,
		ctx:       ctx,
		user:      user,
		inboundID: inboundID,
		sku:       "SKU-" + uuidgql.GenerateV7UUID().String(),
		quantity:  10,
		data:      validData,
	}
}

func (b *itemBuilder) Sku(sku string) *itemBuilder {
	b.sku = sku
	return b
}

func (b *itemBuilder) Quantity(q int64) *itemBuilder {
	b.quantity = q
	return b
}

func (b *itemBuilder) Data(data map[string]any) *itemBuilder {
	b.data = data
	return b
}

func (b *itemBuilder) Deleted() *itemBuilder {
	b.deleted = true
	return b
}

func (b *itemBuilder) Create() *ent.InboundItem {
	b.te.t.Helper()
	var item *ent.InboundItem
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.InboundItem.Create().
			SetTenantID(b.user.TenantID).
			SetInboundID(b.inboundID).
			SetSku(b.sku).
			SetQuantity(b.quantity).
			SetData(b.data).
			SetDataTypeID(itemDataTypeID)

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		item, err = builder.Save(ent.NewTxContext(b.ctx, tx))
		return err
	})
	require.NoError(b.te.t, err)
	return item
}

type notificationBuilder struct {
	te        *testEnv
	ctx       context.Context //nolint:containedctx // Builder pattern for tests
	user      *authn.User
	inboundID uuid.UUID
	data      map[string]any
	deleted   bool
}

func (te *testEnv) newNotification(ctx context.Context, user *authn.User, inboundID uuid.UUID) *notificationBuilder {
	return &notificationBuilder{
		te:        te,
		ctx:       ctx,
		user:      user,
		inboundID: inboundID,
		data:      validData,
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

func (b *notificationBuilder) Create() *ent.InboundShipmentNotification {
	b.te.t.Helper()
	var notification *ent.InboundShipmentNotification
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.InboundShipmentNotification.Create().
			SetTenantID(b.user.TenantID).
			SetInboundID(b.inboundID).
			SetData(b.data).
			SetDataTypeID(itemDataTypeID)

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		notification, err = builder.Save(ent.NewTxContext(b.ctx, tx))
		return err
	})
	require.NoError(b.te.t, err)
	return notification
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
