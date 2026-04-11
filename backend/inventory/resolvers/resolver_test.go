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

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/entityeventsoutbox"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/enttest"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/itemmovement"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	"github.com/pyck-ai/pyck/backend/inventory/resolvers"
	"github.com/pyck-ai/pyck/backend/inventory/services"
)

// =============================================================================
// TEST SETUP
// =============================================================================

func TestMain(m *testing.M) {
	cleanup := resolver.MustSetupTestTracer("inventory")
	defer cleanup()
	os.Exit(m.Run())
}

// =============================================================================
// FIXTURES
// =============================================================================

var (
	tenantA = resolver.TenantA
	tenantB = resolver.TenantB

	// Service-specific user IDs (different from common fixtures)
	userA = &authn.User{
		ID:       uuid.MustParse("0f9049c4-2e95-4246-88b7-e1364b51f7ab"),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_ADMIN},
	}
	userB = &authn.User{
		ID:       uuid.MustParse("4fb2545d-f2b3-4549-acf5-9cd9ff96a272"),
		TenantID: tenantB,
		Roles:    map[uuid.UUID]authn.Role{tenantB: authn.ROLE_ADMIN},
	}

	supplierID = uuid.MustParse("0193547d-d23d-73d1-8300-e9ddf1a26b8d")

	itemDataTypeID           = uuid.MustParse("94a80c62-3f81-4808-8961-768824d2c325")
	itemDataTypeIDUniqueName = uuid.MustParse("14a80c62-3f81-4808-8961-768824d2c325")
	itemDataTypeIDTenantB    = uuid.MustParse("0193547d-d23d-73d1-8300-e9ddf1a26b8d")
	itemDataTypeIDTenantB2   = uuid.MustParse("0193547d-d23d-75b0-89c8-f4edb74e7f87")
	itemDataTypeIDEAN8       = uuid.MustParse("dada54de-b817-48cd-b7ff-69a141373789")
	itemDataTypeIDEAN13      = uuid.MustParse("f3b3572a-d8b1-471e-8f3f-2ef88b8e68f3")
	itemDataTypeIDUPCA       = uuid.MustParse("b72f8792-7f03-4c55-83cf-4d0e29198736")
	itemDataTypeIDUPCE       = uuid.MustParse("b2fedc3a-dcdd-4c4e-b330-7248b93f734f")

	itemDataTypeSlug           = "item"
	itemDataTypeSlugUniqueName = "item_unique_name"
	itemDataTypeSlugEAN8       = "item_ean8"
	itemDataTypeSlugEAN13      = "item_ean13"
	itemDataTypeSlugUPCA       = "item_upca"
	itemDataTypeSlugUPCE       = "item_upce"

	testHandler   = "test-handler"
	testBlockedBy = itemmovement.BlockedByRecalledProducts

	validData = map[string]any{
		"type": "custom",
		"sum":  float64(15),
		"meta": map[string]any{
			"name":   "TestItem",
			"weight": float64(50),
			"tags":   []any{"foo", "bar"},
		},
	}

	validData2 = map[string]any{
		"type": "custom",
		"sum":  float64(20),
		"meta": map[string]any{
			"name":   "TestItem2",
			"weight": float64(100),
			"tags":   []any{"baz", "qux"},
		},
	}

	testItem1 = &ent.Item{
		Sku:          "MK-ENT-X1",
		Data:         validData,
		DataTypeID:   itemDataTypeID,
		DataTypeSlug: itemDataTypeSlug,
	}
	testItem2 = &ent.Item{
		Sku:          "MK-ENT-X2",
		Data:         validData2,
		DataTypeID:   itemDataTypeIDUniqueName,
		DataTypeSlug: itemDataTypeSlugUniqueName,
	}
)

// =============================================================================
// TEST ENVIRONMENT
// =============================================================================

type testEnv struct {
	*resolver.TestEnvironment[*ent.Client]
	t            *testing.T
	StockService *services.InventoryStockService
}

func setup(t *testing.T) *testEnv {
	t.Helper()

	te := &testEnv{
		TestEnvironment: resolver.NewTestEnvironment[*ent.Client](t),
		t:               t,
	}

	dbURI := resolver.DatabaseURI(t)
	client := enttest.Open(t, dialect.SQLite, dbURI,
		enttest.WithOptions(ent.Log(t.Log)),
	)

	if os.Getenv("PYCK_TEST_DEBUG") == "true" {
		client = client.Debug()
	}

	client.Use(events.MutationEventHook(events.HookConfig{
		Service:        "inventory",
		StreamName:     "pyck",
		EntityFetcher:  events.BuildEntityFetcher(ent.TxFromContext, events.FieldData),
		OutboxInserter: events.NewEntOutboxInserter(ent.TxFromContext),
	}))

	inventoryStock, _ := services.NewInventoryStockService()
	te.StockService = inventoryStock
	v := validator.NewValidator(te.DataTypeProvider)
	r := resolvers.NewResolver("inventory", client, v, inventoryStock)
	schema := resolvers.NewSchema(r)

	te.Init(client, schema, func(s *handler.Server) {
		s.Use(gqltx.NewMiddleware(client, ent.NewTxContext, "inventory-test", 0))
	})

	te.loadDataTypes()

	return te
}

// SetupTestEnvironment is a legacy alias for backward compatibility during migration.
func SetupTestEnvironment(t *testing.T) *testEnv {
	t.Helper()
	return setup(t)
}

func (te *testEnv) loadDataTypes() {
	schema1, _ := test.LoadSchemaByName("item")
	schema2, _ := test.LoadSchemaByName("item_unique_name")
	schemaItemEan8, _ := test.LoadSchemaByName("item_ean_8")
	schemaItemEan13, _ := test.LoadSchemaByName("item_ean_13")
	schemaItemUpca, _ := test.LoadSchemaByName("item_upca")
	schemaItemUpce, _ := test.LoadSchemaByName("item_upce")

	te.DataTypeProvider.AddDataType([]json_schema.DataType{
		{
			ID:         itemDataTypeID,
			Slug:       itemDataTypeSlug,
			TenantID:   tenantA,
			JsonSchema: string(schema1),
		}, {
			ID:         itemDataTypeIDUniqueName,
			Slug:       itemDataTypeSlugUniqueName,
			TenantID:   tenantA,
			JsonSchema: string(schema2),
		}, {
			ID:         itemDataTypeIDEAN8,
			Slug:       itemDataTypeSlugEAN8,
			TenantID:   tenantA,
			JsonSchema: string(schemaItemEan8),
		}, {
			ID:         itemDataTypeIDEAN13,
			Slug:       itemDataTypeSlugEAN13,
			TenantID:   tenantA,
			JsonSchema: string(schemaItemEan13),
		}, {
			ID:         itemDataTypeIDUPCA,
			Slug:       itemDataTypeSlugUPCA,
			TenantID:   tenantA,
			JsonSchema: string(schemaItemUpca),
		}, {
			ID:         itemDataTypeIDUPCE,
			Slug:       itemDataTypeSlugUPCE,
			TenantID:   tenantA,
			JsonSchema: string(schemaItemUpce),
		}, {
			ID:         itemDataTypeIDTenantB,
			Slug:       itemDataTypeSlug,
			TenantID:   tenantB,
			JsonSchema: string(schema1),
		}, {
			ID:         itemDataTypeIDTenantB2,
			Slug:       itemDataTypeSlugUniqueName,
			TenantID:   tenantB,
			JsonSchema: string(schema2),
		},
	}...)
}

// ctx creates a request context for the given user.
func (te *testEnv) ctx(user *authn.User) context.Context {
	te.t.Helper()
	ctx := request.Context(te.t.Context(), user, user.TenantID)
	tracer := otelapi.Tracer("inventory-test")
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

// --- Item Builder ---

type itemBuilder struct {
	te           *testEnv
	ctx          context.Context //nolint:containedctx // Builder pattern for tests
	user         *authn.User
	sku          string
	data         map[string]any
	dataTypeID   uuid.UUID
	dataTypeSlug string
	deleted      bool
}

func (te *testEnv) newItem(ctx context.Context, user *authn.User) *itemBuilder {
	return &itemBuilder{
		te:           te,
		ctx:          ctx,
		user:         user,
		sku:          "SKU-" + uuidgql.GenerateV7UUID().String(),
		data:         validData,
		dataTypeID:   itemDataTypeID,
		dataTypeSlug: itemDataTypeSlug,
	}
}

func (b *itemBuilder) Sku(sku string) *itemBuilder {
	b.sku = sku
	return b
}

func (b *itemBuilder) Data(data map[string]any) *itemBuilder {
	b.data = data
	return b
}

func (b *itemBuilder) DataType(id uuid.UUID, slug string) *itemBuilder {
	b.dataTypeID = id
	b.dataTypeSlug = slug
	return b
}

func (b *itemBuilder) Deleted() *itemBuilder {
	b.deleted = true
	return b
}

func (b *itemBuilder) Create() *ent.Item {
	b.te.t.Helper()
	var item *ent.Item
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.Item.Create().
			SetTenantID(b.user.TenantID).
			SetSku(b.sku).
			SetData(b.data).
			SetDataTypeID(b.dataTypeID).
			SetDataTypeSlug(b.dataTypeSlug)

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

// --- Repository Builder ---

type repositoryBuilder struct {
	te          *testEnv
	ctx         context.Context //nolint:containedctx // Builder pattern for tests
	user        *authn.User
	name        string
	repoType    entrepository.Type
	virtualRepo bool
	parentID    *uuid.UUID
	data        map[string]any
	dataTypeID  uuid.UUID
	deleted     bool
}

func (te *testEnv) newRepository(ctx context.Context, user *authn.User) *repositoryBuilder {
	return &repositoryBuilder{
		te:          te,
		ctx:         ctx,
		user:        user,
		name:        "Repo-" + uuidgql.GenerateV7UUID().String(),
		repoType:    entrepository.TypeStatic,
		virtualRepo: false,
		data:        validData,
		dataTypeID:  itemDataTypeID,
	}
}

func (b *repositoryBuilder) Name(name string) *repositoryBuilder {
	b.name = name
	return b
}

func (b *repositoryBuilder) Type(t entrepository.Type) *repositoryBuilder {
	b.repoType = t
	return b
}

func (b *repositoryBuilder) TypeStatic() *repositoryBuilder {
	b.repoType = entrepository.TypeStatic
	return b
}

func (b *repositoryBuilder) TypeDynamic() *repositoryBuilder {
	b.repoType = entrepository.TypeDynamic
	return b
}

func (b *repositoryBuilder) Virtual(v bool) *repositoryBuilder {
	b.virtualRepo = v
	return b
}

func (b *repositoryBuilder) Parent(id uuid.UUID) *repositoryBuilder {
	b.parentID = &id
	return b
}

func (b *repositoryBuilder) Data(data map[string]any) *repositoryBuilder {
	b.data = data
	return b
}

func (b *repositoryBuilder) DataTypeID(id uuid.UUID) *repositoryBuilder {
	b.dataTypeID = id
	return b
}

func (b *repositoryBuilder) NoData() *repositoryBuilder {
	b.data = nil
	b.dataTypeID = uuid.Nil
	return b
}

func (b *repositoryBuilder) Deleted() *repositoryBuilder {
	b.deleted = true
	return b
}

func (b *repositoryBuilder) Create() *ent.Repository {
	b.te.t.Helper()
	var repo *ent.Repository
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.Repository.Create().
			SetTenantID(b.user.TenantID).
			SetName(b.name).
			SetType(b.repoType).
			SetVirtualRepo(b.virtualRepo)

		if b.parentID != nil {
			builder.SetParentID(*b.parentID)
		}
		if b.data != nil {
			builder.SetData(b.data).SetDataTypeID(b.dataTypeID).SetDataTypeSlug(itemDataTypeSlug)
		}
		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		repo, err = builder.Save(ent.NewTxContext(b.ctx, tx))
		return err
	})
	require.NoError(b.te.t, err)
	return repo
}

// --- ItemMovement Builder ---

type itemMovementBuilder struct {
	te         *testEnv
	ctx        context.Context //nolint:containedctx // Builder pattern for tests
	user       *authn.User
	itemID     uuid.UUID
	fromID     uuid.UUID
	toID       uuid.UUID
	handler    string
	blockedBy  itemmovement.BlockedBy
	quantity   int64
	executed   bool
	data       map[string]any
	dataTypeID uuid.UUID
}

func (te *testEnv) newItemMovement(ctx context.Context, user *authn.User, itemID, fromID, toID uuid.UUID) *itemMovementBuilder {
	return &itemMovementBuilder{
		te:         te,
		ctx:        ctx,
		user:       user,
		itemID:     itemID,
		fromID:     fromID,
		toID:       toID,
		handler:    testHandler,
		blockedBy:  testBlockedBy,
		quantity:   10,
		executed:   false,
		data:       validData,
		dataTypeID: itemDataTypeID,
	}
}

func (b *itemMovementBuilder) Handler(h string) *itemMovementBuilder {
	b.handler = h
	return b
}

func (b *itemMovementBuilder) BlockedBy(bb itemmovement.BlockedBy) *itemMovementBuilder {
	b.blockedBy = bb
	return b
}

func (b *itemMovementBuilder) Quantity(q int64) *itemMovementBuilder {
	b.quantity = q
	return b
}

func (b *itemMovementBuilder) Executed(e bool) *itemMovementBuilder {
	b.executed = e
	return b
}

func (b *itemMovementBuilder) Data(data map[string]any) *itemMovementBuilder {
	b.data = data
	return b
}

func (b *itemMovementBuilder) DataTypeID(id uuid.UUID) *itemMovementBuilder {
	b.dataTypeID = id
	return b
}

func (b *itemMovementBuilder) Create() *ent.ItemMovement {
	b.te.t.Helper()
	var mov *ent.ItemMovement
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.ItemMovement.Create().
			SetTenantID(b.user.TenantID).
			SetItemID(b.itemID).
			SetFromID(b.fromID).
			SetToID(b.toID).
			SetHandler(b.handler).
			SetBlockedBy(b.blockedBy).
			SetQuantity(b.quantity).
			SetExecuted(b.executed).
			SetData(b.data).
			SetDataTypeID(b.dataTypeID)

		var err error
		mov, err = builder.Save(ent.NewTxContext(b.ctx, tx))
		return err
	})
	require.NoError(b.te.t, err)
	return mov
}

// --- Stock Builder ---

type stockBuilder struct {
	te           *testEnv
	ctx          context.Context //nolint:containedctx // Builder pattern for tests
	user         *authn.User
	itemID       uuid.UUID
	repositoryID uuid.UUID
	quantity     int64
	incoming     int64
	outgoing     int64
	movementID   uuid.UUID
}

func (te *testEnv) newStock(ctx context.Context, user *authn.User, itemID, repoID uuid.UUID) *stockBuilder {
	return &stockBuilder{
		te:           te,
		ctx:          ctx,
		user:         user,
		itemID:       itemID,
		repositoryID: repoID,
		quantity:     100,
		incoming:     0,
		outgoing:     0,
		movementID:   itemID, // default to itemID
	}
}

func (b *stockBuilder) Quantity(q int64) *stockBuilder {
	b.quantity = q
	return b
}

func (b *stockBuilder) Incoming(i int64) *stockBuilder {
	b.incoming = i
	return b
}

func (b *stockBuilder) Outgoing(o int64) *stockBuilder {
	b.outgoing = o
	return b
}

func (b *stockBuilder) MovementID(id uuid.UUID) *stockBuilder {
	b.movementID = id
	return b
}

func (b *stockBuilder) Create() *ent.Stock {
	b.te.t.Helper()
	var stock *ent.Stock
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.Stock.Create().
			SetTenantID(b.user.TenantID).
			SetItemID(b.itemID).
			SetRepositoryID(b.repositoryID).
			SetQuantity(b.quantity).
			SetIncomingStock(b.incoming).
			SetOutgoingStock(b.outgoing).
			SetMovementID(b.movementID)

		var err error
		stock, err = builder.Save(ent.NewTxContext(b.ctx, tx))
		return err
	})
	require.NoError(b.te.t, err)
	return stock
}

// --- ItemSet Builder ---

type itemSetBuilder struct {
	te         *testEnv
	ctx        context.Context //nolint:containedctx // Builder pattern for tests
	user       *authn.User
	sku        string
	data       map[string]any
	dataTypeID uuid.UUID
	deleted    bool
}

func (te *testEnv) newItemSet(ctx context.Context, user *authn.User) *itemSetBuilder {
	return &itemSetBuilder{
		te:         te,
		ctx:        ctx,
		user:       user,
		sku:        "ITEMSET-" + uuidgql.GenerateV7UUID().String(),
		data:       validData,
		dataTypeID: itemDataTypeID,
	}
}

func (b *itemSetBuilder) Sku(sku string) *itemSetBuilder {
	b.sku = sku
	return b
}

func (b *itemSetBuilder) Data(data map[string]any) *itemSetBuilder {
	b.data = data
	return b
}

func (b *itemSetBuilder) DataTypeID(id uuid.UUID) *itemSetBuilder {
	b.dataTypeID = id
	return b
}

func (b *itemSetBuilder) Deleted() *itemSetBuilder {
	b.deleted = true
	return b
}

func (b *itemSetBuilder) Create() *ent.ItemSet {
	b.te.t.Helper()
	var itemSet *ent.ItemSet
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.ItemSet.Create().
			SetTenantID(b.user.TenantID).
			SetSku(b.sku).
			SetData(b.data).
			SetDataTypeID(b.dataTypeID)

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		itemSet, err = builder.Save(ent.NewTxContext(b.ctx, tx))
		return err
	})
	require.NoError(b.te.t, err)
	return itemSet
}

// --- ReplenishmentOrder Builder ---

type replenishmentOrderBuilder struct {
	te         *testEnv
	ctx        context.Context //nolint:containedctx // Builder pattern for tests
	user       *authn.User
	supplierID *uuid.UUID
	data       map[string]any
	dataTypeID uuid.UUID
	deleted    bool
}

func (te *testEnv) newReplenishmentOrder(ctx context.Context, user *authn.User) *replenishmentOrderBuilder {
	return &replenishmentOrderBuilder{
		te:         te,
		ctx:        ctx,
		user:       user,
		supplierID: &supplierID,
		data:       validData,
		dataTypeID: itemDataTypeID,
	}
}

func (b *replenishmentOrderBuilder) SupplierID(id uuid.UUID) *replenishmentOrderBuilder {
	b.supplierID = &id
	return b
}

func (b *replenishmentOrderBuilder) NoSupplier() *replenishmentOrderBuilder {
	b.supplierID = nil
	return b
}

func (b *replenishmentOrderBuilder) Data(data map[string]any) *replenishmentOrderBuilder {
	b.data = data
	return b
}

func (b *replenishmentOrderBuilder) DataTypeID(id uuid.UUID) *replenishmentOrderBuilder {
	b.dataTypeID = id
	return b
}

func (b *replenishmentOrderBuilder) Deleted() *replenishmentOrderBuilder {
	b.deleted = true
	return b
}

func (b *replenishmentOrderBuilder) Create() *ent.ReplenishmentOrder {
	b.te.t.Helper()
	var order *ent.ReplenishmentOrder
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.ReplenishmentOrder.Create().
			SetTenantID(b.user.TenantID).
			SetData(b.data).
			SetDataTypeID(b.dataTypeID)

		if b.supplierID != nil {
			builder.SetSupplierID(*b.supplierID)
		}
		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		order, err = builder.Save(ent.NewTxContext(b.ctx, tx))
		return err
	})
	require.NoError(b.te.t, err)
	return order
}

// --- RepositoryMovement Builder ---

type repositoryMovementBuilder struct {
	te           *testEnv
	ctx          context.Context //nolint:containedctx // Builder pattern for tests
	user         *authn.User
	repositoryID uuid.UUID
	fromID       *uuid.UUID
	toID         uuid.UUID
	handler      string
	executed     bool
	data         map[string]any
	dataTypeID   uuid.UUID
	deleted      bool
}

func (te *testEnv) newRepositoryMovement(ctx context.Context, user *authn.User, repositoryID, toID uuid.UUID) *repositoryMovementBuilder {
	return &repositoryMovementBuilder{
		te:           te,
		ctx:          ctx,
		user:         user,
		repositoryID: repositoryID,
		toID:         toID,
		handler:      testHandler,
		executed:     false,
		data:         validData,
		dataTypeID:   itemDataTypeID,
	}
}

func (b *repositoryMovementBuilder) FromID(id uuid.UUID) *repositoryMovementBuilder {
	b.fromID = &id
	return b
}

func (b *repositoryMovementBuilder) Handler(h string) *repositoryMovementBuilder {
	b.handler = h
	return b
}

func (b *repositoryMovementBuilder) Executed(e bool) *repositoryMovementBuilder {
	b.executed = e
	return b
}

func (b *repositoryMovementBuilder) Data(data map[string]any) *repositoryMovementBuilder {
	b.data = data
	return b
}

func (b *repositoryMovementBuilder) DataTypeID(id uuid.UUID) *repositoryMovementBuilder {
	b.dataTypeID = id
	return b
}

func (b *repositoryMovementBuilder) NoData() *repositoryMovementBuilder {
	b.data = nil
	b.dataTypeID = uuid.Nil
	return b
}

func (b *repositoryMovementBuilder) Deleted() *repositoryMovementBuilder {
	b.deleted = true
	return b
}

func (b *repositoryMovementBuilder) Create() *ent.RepositoryMovement {
	b.te.t.Helper()
	var mov *ent.RepositoryMovement
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.RepositoryMovement.Create().
			SetTenantID(b.user.TenantID).
			SetRepositoryID(b.repositoryID).
			SetToID(b.toID).
			SetHandler(b.handler).
			SetExecuted(b.executed)

		if b.fromID != nil {
			builder.SetFromID(*b.fromID)
		}
		if b.data != nil {
			builder.SetData(b.data).SetDataTypeID(b.dataTypeID)
		}
		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		mov, err = builder.Save(ent.NewTxContext(b.ctx, tx))
		return err
	})
	require.NoError(b.te.t, err)
	return mov
}

// --- CollectionMovement Builder ---

type collectionMovementBuilder struct {
	te         *testEnv
	ctx        context.Context //nolint:containedctx // Builder pattern for tests
	user       *authn.User
	handler    string
	data       map[string]any
	dataTypeID uuid.UUID
	deleted    bool
}

func (te *testEnv) newCollectionMovement(ctx context.Context, user *authn.User) *collectionMovementBuilder {
	return &collectionMovementBuilder{
		te:         te,
		ctx:        ctx,
		user:       user,
		handler:    testHandler,
		data:       validData,
		dataTypeID: itemDataTypeID,
	}
}

func (b *collectionMovementBuilder) Handler(h string) *collectionMovementBuilder {
	b.handler = h
	return b
}

func (b *collectionMovementBuilder) Data(data map[string]any) *collectionMovementBuilder {
	b.data = data
	return b
}

func (b *collectionMovementBuilder) DataTypeID(id uuid.UUID) *collectionMovementBuilder {
	b.dataTypeID = id
	return b
}

func (b *collectionMovementBuilder) Deleted() *collectionMovementBuilder {
	b.deleted = true
	return b
}

func (b *collectionMovementBuilder) Create() *ent.Collection_Movement {
	b.te.t.Helper()
	var mov *ent.Collection_Movement
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.Collection_Movement.Create().
			SetTenantID(b.user.TenantID).
			SetHandler(b.handler).
			SetData(b.data).
			SetDataTypeID(b.dataTypeID)

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		mov, err = builder.Save(ent.NewTxContext(b.ctx, tx))
		return err
	})
	require.NoError(b.te.t, err)
	return mov
}

// =============================================================================
// TRANSACTION HELPER
// =============================================================================

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

// assertEventCounts verifies the outbox contains the expected number of events per entity type.
// Topic format: pyck.{tenant}.crud.inventory.{schema}.{id}.{op}
// The schema is extracted from index 4 of the dot-separated topic.
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
