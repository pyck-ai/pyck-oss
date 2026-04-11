package resolvers_test

import (
	"context"
	"embed"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
)

//go:embed testdata/tenant-isolation/*.test.yaml
var tenantTestdata embed.FS

// =============================================================================
// GRAPHQL TEMPLATES (only those not already defined in other test files)
// =============================================================================

var (
	deleteCollectionMovement = testresolver.ParseTemplate(`mutation {
		deleteInventoryCollection(id: "{{.ID}}") { deletedID }
	}`)

	createReplenishmentOrderItemTpl = testresolver.ParseTemplate(`mutation {
		createReplenishmentOrderItem(input: {
			replenishmentorderID: "{{.OrderID}}",
			sku: "{{.Sku}}",
			quantity: {{.Quantity}}
		}) {
			replenishmentOrderItem { id tenantID sku quantity }
		}
	}`)

	updateReplenishmentOrderItemTpl = testresolver.ParseTemplate(`mutation {
		updateReplenishmentOrderItem(id: "{{.ID}}", input: {
			quantity: {{or .Quantity 999}}
		}) {
			replenishmentOrderItem { id tenantID sku quantity }
		}
	}`)

	deleteReplenishmentOrderItemTpl = testresolver.ParseTemplate(`mutation {
		deleteReplenishmentOrderItem(id: "{{.ID}}") { deletedID }
	}`)
)

// =============================================================================
// TYPES
// =============================================================================

// --- GraphQL response types ---

type createReplenishmentOrderItemData struct {
	CreateReplenishmentOrderItem struct {
		ReplenishmentOrderItem struct {
			ID       uuid.UUID
			TenantID uuid.UUID
			Sku      string
			Quantity int
		}
	}
}

// --- YAML scenario types ---

type (
	tenantScenario struct {
		Name        string        `yaml:"name"`
		Description string        `yaml:"description"`
		Seed        tenantSeed    `yaml:"seed"`
		Checks      []tenantCheck `yaml:"checks"`
	}

	tenantSeed struct {
		Tenants                 []tenantDef           `yaml:"tenants"`
		Repositories            []repoSeed            `yaml:"repositories"`
		Items                   []itemSeed            `yaml:"items"`
		ItemMovements           []itemMovementSeed    `yaml:"itemMovements"`
		ExecuteMovements        []executeMovementSeed `yaml:"executeMovements"`
		RepositoryMovements     []repoMovementSeed    `yaml:"repositoryMovements"`
		CollectionMovements     []collectionMovSeed   `yaml:"collectionMovements"`
		ItemSets                []itemSetSeed         `yaml:"itemSets"`
		ReplenishmentOrders     []replOrderSeed       `yaml:"replenishmentOrders"`
		ReplenishmentOrderItems []replOrderItemSeed   `yaml:"replenishmentOrderItems"`
	}

	tenantDef struct {
		Name string `yaml:"name"`
	}

	tenantCheck struct {
		Type           string         `yaml:"type"`
		Entity         string         `yaml:"entity"`
		Tenant         string         `yaml:"tenant"`
		Target         string         `yaml:"target"`
		Description    string         `yaml:"description"`
		ExpectCount    int            `yaml:"expectCount"`
		ExpectError    string         `yaml:"expectError"`
		Repository     string         `yaml:"repository"`
		Item           string         `yaml:"item"`
		ExpectQuantity int64          `yaml:"expectQuantity"`
		Args           map[string]any `yaml:"args"`
	}
)

// --- Seed entity types ---

type (
	repoSeed struct {
		Name    string `yaml:"name"`
		Tenant  string `yaml:"tenant"`
		Type    string `yaml:"type"`
		Virtual bool   `yaml:"virtual"`
		Parent  string `yaml:"parent"`
	}

	itemSeed struct {
		Sku    string `yaml:"sku"`
		Tenant string `yaml:"tenant"`
	}

	itemMovementSeed struct {
		Name     string `yaml:"name"`
		Tenant   string `yaml:"tenant"`
		Item     string `yaml:"item"`
		From     string `yaml:"from"`
		To       string `yaml:"to"`
		Quantity int64  `yaml:"quantity"`
		Handler  string `yaml:"handler"`
		Execute  bool   `yaml:"execute"`
	}

	executeMovementSeed struct {
		Tenant   string `yaml:"tenant"`
		Movement string `yaml:"movement"`
	}

	repoMovementSeed struct {
		Name       string `yaml:"name"`
		Tenant     string `yaml:"tenant"`
		Repository string `yaml:"repository"`
		From       string `yaml:"from"`
		To         string `yaml:"to"`
		Handler    string `yaml:"handler"`
	}

	collectionMovSeed struct {
		Name       string                `yaml:"name"`
		Tenant     string                `yaml:"tenant"`
		Collection []collectionEntrySeed `yaml:"collection"`
	}

	collectionEntrySeed struct {
		Item     string `yaml:"item"`
		From     string `yaml:"from"`
		To       string `yaml:"to"`
		Quantity int64  `yaml:"quantity"`
		Handler  string `yaml:"handler"`
	}

	itemSetSeed struct {
		Name   string   `yaml:"name"`
		Tenant string   `yaml:"tenant"`
		Sku    string   `yaml:"sku"`
		Items  []string `yaml:"items"`
	}

	replOrderSeed struct {
		Name   string `yaml:"name"`
		Tenant string `yaml:"tenant"`
	}

	replOrderItemSeed struct {
		Name     string `yaml:"name"`
		Tenant   string `yaml:"tenant"`
		Order    string `yaml:"order"`
		Sku      string `yaml:"sku"`
		Quantity int64  `yaml:"quantity"`
	}
)

// =============================================================================
// TENANT FIXTURES
// =============================================================================

var tenantUsers = map[string]*authn.User{
	"alpha": userA,
	"beta":  userB,
}

// =============================================================================
// TEST RUNNER
// =============================================================================

func TestTenantIsolation(t *testing.T) {
	t.Parallel()

	scenarios := loadTenantScenarios(t)

	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)

			for _, td := range scenario.Seed.Tenants {
				_ = mustTenantUser(t, td.Name)
			}

			entityIDs := make(map[string]uuid.UUID)

			// Seed phase (order matters for dependencies).
			seedRepositories(t, te, scenario.Seed.Repositories, entityIDs)
			seedItems(t, te, scenario.Seed.Items, entityIDs)
			seedItemMovements(t, te, scenario.Seed.ItemMovements, entityIDs)
			seedExecuteMovements(t, te, scenario.Seed.ExecuteMovements, entityIDs)
			seedRepositoryMovements(t, te, scenario.Seed.RepositoryMovements, entityIDs)
			seedCollectionMovements(t, te, scenario.Seed.CollectionMovements, entityIDs)
			seedItemSets(t, te, scenario.Seed.ItemSets, entityIDs)
			seedReplenishmentOrders(t, te, scenario.Seed.ReplenishmentOrders, entityIDs)
			seedReplenishmentOrderItems(t, te, scenario.Seed.ReplenishmentOrderItems, entityIDs)

			// Check phase.
			for _, check := range scenario.Checks {
				t.Run(check.Description, func(t *testing.T) {
					ctx := te.ctx(mustTenantUser(t, check.Tenant))

					switch check.Type {
					case "query":
						checkQuery(t, te, ctx, check)
					case "query-stock":
						checkStock(t, te, ctx, check, entityIDs)
					case "cross-update":
						checkCrossUpdate(t, te, ctx, check, entityIDs)
					case "cross-delete":
						checkCrossDelete(t, te, ctx, check, entityIDs)
					case "cross-execute":
						checkCrossExecute(t, te, ctx, check, entityIDs)
					case "cross-create":
						checkCrossCreate(t, te, ctx, check, entityIDs)
					case "cross-update-with-args":
						checkCrossUpdateWithArgs(t, te, ctx, check, entityIDs)
					default:
						t.Fatalf("unknown check type %q", check.Type)
					}
				})
			}
		})
	}
}

// =============================================================================
// SEED HELPERS
// =============================================================================

func seedRepositories(t *testing.T, te *testEnv, repos []repoSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, rs := range repos {
		user := mustTenantUser(t, rs.Tenant)
		ctx := te.ctx(user)
		b := te.newRepository(ctx, user).Name(rs.Name).Virtual(rs.Virtual).NoData()

		switch rs.Type {
		case "static":
			b.Type(entrepository.TypeStatic)
		case "dynamic":
			b.Type(entrepository.TypeDynamic)
		default:
			t.Fatalf("unknown repository type %q", rs.Type)
		}

		if rs.Parent != "" {
			b.Parent(mustResolve(t, ids, rs.Tenant+"/"+rs.Parent))
		}

		key := rs.Tenant + "/" + rs.Name
		ids[key] = b.Create().ID
		t.Logf("Seeded repository: %s (id=%s)", key, ids[key])
	}
}

func seedItems(t *testing.T, te *testEnv, items []itemSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, is := range items {
		user := mustTenantUser(t, is.Tenant)
		key := is.Tenant + "/" + is.Sku
		ids[key] = te.newItem(te.ctx(user), user).Sku(is.Sku).Create().ID
		t.Logf("Seeded item: %s (id=%s)", key, ids[key])
	}
}

func seedItemMovements(t *testing.T, te *testEnv, movements []itemMovementSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, ms := range movements {
		user := mustTenantUser(t, ms.Tenant)
		mov := te.newItemMovement(te.ctx(user), user,
			mustResolve(t, ids, ms.Tenant+"/"+ms.Item),
			mustResolve(t, ids, ms.Tenant+"/"+ms.From),
			mustResolve(t, ids, ms.Tenant+"/"+ms.To),
		).Quantity(ms.Quantity).Handler(ms.Handler).Executed(ms.Execute).Create()

		key := ms.Tenant + "/" + ms.Name
		ids[key] = mov.ID
		t.Logf("Seeded item movement: %s (id=%s, executed=%v)", key, mov.ID, ms.Execute)
	}
}

func seedExecuteMovements(t *testing.T, te *testEnv, executions []executeMovementSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, es := range executions {
		user := mustTenantUser(t, es.Tenant)
		movID := mustResolve(t, ids, es.Tenant+"/"+es.Movement)
		execOK[executeItemMovementData](te, te.ctx(user), executeItemMovement, map[string]any{"ID": movID})
		t.Logf("Executed item movement: %s/%s (id=%s)", es.Tenant, es.Movement, movID)
	}
}

func seedRepositoryMovements(t *testing.T, te *testEnv, movements []repoMovementSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, ms := range movements {
		user := mustTenantUser(t, ms.Tenant)
		ctx := te.ctx(user)

		b := te.newRepositoryMovement(ctx, user,
			mustResolve(t, ids, ms.Tenant+"/"+ms.Repository),
			mustResolve(t, ids, ms.Tenant+"/"+ms.To),
		).Handler(ms.Handler).NoData()

		if ms.From != "" {
			b.FromID(mustResolve(t, ids, ms.Tenant+"/"+ms.From))
		}

		key := ms.Tenant + "/" + ms.Name
		ids[key] = b.Create().ID
		t.Logf("Seeded repository movement: %s (id=%s)", key, ids[key])
	}
}

func seedCollectionMovements(t *testing.T, te *testEnv, collections []collectionMovSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, cs := range collections {
		user := mustTenantUser(t, cs.Tenant)
		entries := buildCollectionEntries(t, cs.Tenant, cs.Collection, ids)

		data := execOK[createCollectionMovementData](te, te.ctx(user), createCollectionMovement, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Collection": entries,
		})

		key := cs.Tenant + "/" + cs.Name
		ids[key] = data.CreateInventoryCollectionMovement.ID
		t.Logf("Seeded collection movement: %s (id=%s, entries=%d)", key, ids[key], len(entries))
	}
}

func seedItemSets(t *testing.T, te *testEnv, sets []itemSetSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, ss := range sets {
		user := mustTenantUser(t, ss.Tenant)
		ctx := te.ctx(user)
		itemSet := te.newItemSet(ctx, user).Sku(ss.Sku).Create()

		if len(ss.Items) > 0 {
			var itemIDs []uuid.UUID
			for _, sku := range ss.Items {
				itemIDs = append(itemIDs, mustResolve(t, ids, ss.Tenant+"/"+sku))
			}
			err := te.withTx(ctx, func(tx *ent.Tx) error {
				return tx.ItemSet.UpdateOneID(itemSet.ID).AddItemIDs(itemIDs...).Exec(ent.NewTxContext(ctx, tx))
			})
			require.NoError(t, err, "failed to add items to item set %q", ss.Name)
		}

		key := ss.Tenant + "/" + ss.Name
		ids[key] = itemSet.ID
		t.Logf("Seeded item set: %s (id=%s, items=%d)", key, itemSet.ID, len(ss.Items))
	}
}

func seedReplenishmentOrders(t *testing.T, te *testEnv, orders []replOrderSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, os := range orders {
		user := mustTenantUser(t, os.Tenant)
		key := os.Tenant + "/" + os.Name
		ids[key] = te.newReplenishmentOrder(te.ctx(user), user).Create().ID
		t.Logf("Seeded replenishment order: %s (id=%s)", key, ids[key])
	}
}

func seedReplenishmentOrderItems(t *testing.T, te *testEnv, items []replOrderItemSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, ri := range items {
		user := mustTenantUser(t, ri.Tenant)
		data := execOK[createReplenishmentOrderItemData](te, te.ctx(user), createReplenishmentOrderItemTpl, map[string]any{
			"OrderID":  mustResolve(t, ids, ri.Tenant+"/"+ri.Order),
			"Sku":      ri.Sku,
			"Quantity": ri.Quantity,
		})

		key := ri.Tenant + "/" + ri.Name
		ids[key] = data.CreateReplenishmentOrderItem.ReplenishmentOrderItem.ID
		t.Logf("Seeded replenishment order item: %s (id=%s)", key, ids[key])
	}
}

// =============================================================================
// CHECK HANDLERS
// =============================================================================

func checkQuery(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck) {
	t.Helper()

	switch check.Entity {
	case "repository":
		data := execOK[queryRepositoriesData](te, ctx, queryRepositories, nil)
		assert.Equal(t, check.ExpectCount, data.Repositories.TotalCount)
	case "item":
		data := execOK[queryItemsData](te, ctx, queryItems, nil)
		assert.Equal(t, check.ExpectCount, data.InventoryItems.TotalCount)
	case "itemMovement":
		data := execOK[queryItemMovementsData](te, ctx, queryItemMovements, nil)
		assert.Equal(t, check.ExpectCount, data.ItemMovements.TotalCount)
	case "repositoryMovement":
		data := execOK[queryRepositoryMovementsData](te, ctx, queryRepositoryMovements, nil)
		assert.Equal(t, check.ExpectCount, data.RepositoryMovements.TotalCount)
	case "collectionMovement":
		data := execOK[queryCollectionsData](te, ctx, queryCollections, nil)
		assert.Equal(t, check.ExpectCount, data.InventoryCollections.TotalCount)
	case "itemSet":
		data := execOK[queryItemSetsData](te, ctx, queryItemSets, nil)
		assert.Equal(t, check.ExpectCount, data.InventoryItemSets.TotalCount)
	case "replenishmentOrder":
		data := execOK[queryReplenishmentOrdersData](te, ctx, queryReplenishmentOrdersTpl, nil)
		assert.Equal(t, check.ExpectCount, data.ReplenishmentOrders.TotalCount)
	case "replenishmentOrderItem":
		data := execOK[queryReplenishmentOrderItemsData](te, ctx, queryReplenishmentOrderItemsTpl, nil)
		assert.Equal(t, check.ExpectCount, data.ReplenishmentOrderItems.TotalCount)
	case "stock":
		data := execOK[stocksData](te, ctx, stocksQueryTemplate, nil)
		assert.Equal(t, check.ExpectCount, data.Stocks.TotalCount)
	case "transaction":
		data := execOK[transactionsData](te, ctx, queryTransactions, nil)
		assert.Equal(t, check.ExpectCount, data.Transactions.TotalCount)
	default:
		t.Fatalf("unsupported entity %q for query check", check.Entity)
	}
}

func checkStock(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()

	repoID := mustResolve(t, ids, check.Repository)
	itemID := mustResolve(t, ids, check.Item)

	where := fmt.Sprintf(`{ repositoryID: "%s", itemID: "%s" }`, repoID, itemID)
	data := execOK[stocksData](te, ctx, stocksQueryTemplate, map[string]any{"Where": where})

	require.NotEmpty(t, data.Stocks.Edges, "expected stock for repo=%s item=%s", check.Repository, check.Item)
	assert.Equal(t, check.ExpectQuantity, data.Stocks.Edges[0].Node.Quantity)
}

func checkCrossUpdate(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	targetID := mustResolve(t, ids, check.Target)

	switch check.Entity {
	case "repository":
		execErr(te, ctx, updateRepository, map[string]any{"ID": targetID, "Name": "cross-tenant-test"}, check.ExpectError)
	case "item":
		execErr(te, ctx, updateItem, map[string]any{"ID": targetID, "DataTypeID": itemDataTypeID}, check.ExpectError)
	case "itemMovement":
		execErr(te, ctx, updateItemMovement, map[string]any{"ID": targetID, "Handler": "cross-tenant-test"}, check.ExpectError)
	case "repositoryMovement":
		execErr(te, ctx, updateRepositoryMovement, map[string]any{"ID": targetID, "DataTypeID": itemDataTypeID, "Data": true}, check.ExpectError)
	case "collectionMovement":
		execErr(te, ctx, updateCollectionMovement, map[string]any{"ID": targetID, "Handler": "cross-tenant-test"}, check.ExpectError)
	case "itemSet":
		execErr(te, ctx, updateItemSet, map[string]any{"ID": targetID, "ItemID": targetID}, check.ExpectError)
	case "replenishmentOrder":
		execErr(te, ctx, updateReplenishmentOrderTpl, map[string]any{"ID": targetID, "DataTypeID": itemDataTypeID}, check.ExpectError)
	case "replenishmentOrderItem":
		execErr(te, ctx, updateReplenishmentOrderItemTpl, map[string]any{"ID": targetID, "Quantity": 999}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-update check", check.Entity)
	}
}

func checkCrossDelete(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	targetID := mustResolve(t, ids, check.Target)

	switch check.Entity {
	case "repository":
		execErr(te, ctx, deleteRepository, map[string]any{"ID": targetID}, check.ExpectError)
	case "item":
		execErr(te, ctx, deleteItem, map[string]any{"ID": targetID}, check.ExpectError)
	case "itemMovement":
		execErr(te, ctx, deleteItemMovement, map[string]any{"ID": targetID}, check.ExpectError)
	case "repositoryMovement":
		execErr(te, ctx, deleteRepositoryMovement, map[string]any{"ID": targetID}, check.ExpectError)
	case "collectionMovement":
		execErr(te, ctx, deleteCollectionMovement, map[string]any{"ID": targetID}, check.ExpectError)
	case "itemSet":
		execErr(te, ctx, deleteItemSet, map[string]any{"ID": targetID}, check.ExpectError)
	case "replenishmentOrder":
		execErr(te, ctx, deleteReplenishmentOrderTpl, map[string]any{"ID": targetID}, check.ExpectError)
	case "replenishmentOrderItem":
		execErr(te, ctx, deleteReplenishmentOrderItemTpl, map[string]any{"ID": targetID}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-delete check", check.Entity)
	}
}

func checkCrossExecute(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	targetID := mustResolve(t, ids, check.Target)

	switch check.Entity {
	case "itemMovement":
		execErr(te, ctx, executeItemMovement, map[string]any{"ID": targetID}, check.ExpectError)
	case "repositoryMovement":
		execErr(te, ctx, executeRepositoryMovement, map[string]any{"ID": targetID}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-execute check", check.Entity)
	}
}

func checkCrossCreate(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()

	switch check.Entity {
	case "itemMovement":
		execErr(te, ctx, createItemMovement, map[string]any{
			"ItemID":     mustResolve(t, ids, mustArg(t, check.Args, "item")),
			"FromID":     mustResolve(t, ids, mustArg(t, check.Args, "from")),
			"ToID":       mustResolve(t, ids, mustArg(t, check.Args, "to")),
			"Quantity":   mustArgInt(t, check.Args, "quantity"),
			"Handler":    mustArg(t, check.Args, "handler"),
			"BlockedBy":  testBlockedBy,
			"DataTypeID": itemDataTypeID,
		}, check.ExpectError)

	case "repositoryMovement":
		execErr(te, ctx, createRepositoryMovement, map[string]any{
			"RepositoryID": mustResolve(t, ids, mustArg(t, check.Args, "repository")),
			"FromID":       mustResolve(t, ids, mustArg(t, check.Args, "from")),
			"ToID":         mustResolve(t, ids, mustArg(t, check.Args, "to")),
			"Handler":      mustArg(t, check.Args, "handler"),
			"Executed":     false,
		}, check.ExpectError)

	case "collectionMovement":
		entries := buildCollectionEntriesFromArgs(t, check.Args, ids)
		execErr(te, ctx, createCollectionMovement, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Collection": entries,
		}, check.ExpectError)

	case "itemSet":
		itemRefs := mustArgSlice(t, check.Args, "items")
		require.NotEmpty(t, itemRefs, "cross-create itemSet needs at least one item")
		execErr(te, ctx, createItemSet, map[string]any{
			"Sku":        mustArg(t, check.Args, "sku"),
			"ItemID":     mustResolve(t, ids, itemRefs[0].(string)),
			"DataTypeID": itemDataTypeID,
		}, check.ExpectError)

	case "replenishmentOrder":
		args := map[string]any{
			"SupplierID": mustResolve(t, ids, mustArg(t, check.Args, "supplierID")),
			"DataTypeID": itemDataTypeID,
		}
		if check.ExpectError == "none" {
			execOK[any](te, ctx, createReplenishmentOrderTpl, args)
		} else {
			execErr(te, ctx, createReplenishmentOrderTpl, args, check.ExpectError)
		}

	case "replenishmentOrderItem":
		execErr(te, ctx, createReplenishmentOrderItemTpl, map[string]any{
			"OrderID":  mustResolve(t, ids, mustArg(t, check.Args, "order")),
			"Sku":      mustArg(t, check.Args, "sku"),
			"Quantity": mustArgInt(t, check.Args, "quantity"),
		}, check.ExpectError)

	default:
		t.Fatalf("unsupported entity %q for cross-create check", check.Entity)
	}
}

func checkCrossUpdateWithArgs(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	targetID := mustResolve(t, ids, check.Target)

	switch check.Entity {
	case "replenishmentOrder":
		execErr(te, ctx, updateReplenishmentOrderTpl, map[string]any{
			"ID":         targetID,
			"SupplierID": mustResolve(t, ids, mustArg(t, check.Args, "supplierID")),
		}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-update-with-args check", check.Entity)
	}
}

// =============================================================================
// PRIVATE HELPERS
// =============================================================================

func mustTenantUser(t *testing.T, name string) *authn.User {
	t.Helper()
	user, ok := tenantUsers[name]
	require.True(t, ok, "unknown tenant %q", name)
	return user
}

func mustResolve(t *testing.T, ids map[string]uuid.UUID, key string) uuid.UUID {
	t.Helper()
	id, ok := ids[key]
	require.True(t, ok, "entity %q not found (available: %v)", key, mapKeys(ids))
	return id
}

func mustArg(t *testing.T, args map[string]any, key string) string {
	t.Helper()
	v, ok := args[key]
	require.True(t, ok, "missing arg %q", key)
	s, ok := v.(string)
	require.True(t, ok, "arg %q must be string, got %T", key, v)
	return s
}

func mustArgInt(t *testing.T, args map[string]any, key string) int {
	t.Helper()
	v, ok := args[key]
	require.True(t, ok, "missing arg %q", key)
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		t.Fatalf("arg %q must be number, got %T", key, v)
		return 0
	}
}

func mustArgSlice(t *testing.T, args map[string]any, key string) []any {
	t.Helper()
	v, ok := args[key]
	require.True(t, ok, "missing arg %q", key)
	s, ok := v.([]interface{})
	require.True(t, ok, "arg %q must be list, got %T", key, v)
	return s
}

func mapKeys(m map[string]uuid.UUID) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func buildCollectionEntries(t *testing.T, tenant string, entries []collectionEntrySeed, ids map[string]uuid.UUID) []map[string]any {
	t.Helper()
	result := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		result = append(result, map[string]any{
			"ItemID":     mustResolve(t, ids, tenant+"/"+e.Item),
			"FromID":     mustResolve(t, ids, tenant+"/"+e.From),
			"ToID":       mustResolve(t, ids, tenant+"/"+e.To),
			"Quantity":   e.Quantity,
			"Handler":    e.Handler,
			"DataTypeID": itemDataTypeID,
		})
	}
	return result
}

func buildCollectionEntriesFromArgs(t *testing.T, args map[string]any, ids map[string]uuid.UUID) []map[string]any {
	t.Helper()
	raw := mustArgSlice(t, args, "collection")
	result := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		entry, ok := r.(map[string]any)
		require.True(t, ok, "collection entry must be a map")
		result = append(result, map[string]any{
			"ItemID":     mustResolve(t, ids, entry["item"].(string)),
			"FromID":     mustResolve(t, ids, entry["from"].(string)),
			"ToID":       mustResolve(t, ids, entry["to"].(string)),
			"Quantity":   entry["quantity"],
			"Handler":    entry["handler"],
			"DataTypeID": itemDataTypeID,
		})
	}
	return result
}

func loadTenantScenarios(t *testing.T) []tenantScenario {
	t.Helper()

	entries, err := tenantTestdata.ReadDir("testdata/tenant-isolation")
	require.NoError(t, err, "failed to read testdata/tenant-isolation directory")

	var scenarios []tenantScenario
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".test.yaml") {
			continue
		}
		data, err := tenantTestdata.ReadFile(filepath.Join("testdata/tenant-isolation", entry.Name()))
		require.NoError(t, err, "failed to read %s", entry.Name())

		var scenario tenantScenario
		err = yaml.Unmarshal(data, &scenario)
		require.NoError(t, err, "failed to parse %s", entry.Name())

		if scenario.Name == "" {
			scenario.Name = strings.TrimSuffix(entry.Name(), ".test.yaml")
		}

		scenarios = append(scenarios, scenario)
		t.Logf("Loaded tenant scenario: %s (%s)", scenario.Name, entry.Name())
	}

	require.NotEmpty(t, scenarios, "no tenant test scenarios found")
	return scenarios
}
