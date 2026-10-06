package resolvers_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/validator"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entcollection_movement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/collection_movement"
	entitem "github.com/pyck-ai/pyck/backend/inventory/ent/gen/item"
	entitemmovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/itemmovement"
	entitemset "github.com/pyck-ai/pyck/backend/inventory/ent/gen/itemset"
	entreplenishmentorder "github.com/pyck-ai/pyck/backend/inventory/ent/gen/replenishmentorder"
	entreplenishmentorderitem "github.com/pyck-ai/pyck/backend/inventory/ent/gen/replenishmentorderitem"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	entrepositorymovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repositorymovement"
)

// The templates below carry the data payload as a pre-rendered GraphQL
// literal (see uniqueNameData) so one template serves every unique value a
// test needs; the shared templates in the entity test files hard-code theirs.
var (
	uniqueCreateItem = resolver.ParseTemplate(`mutation {
		createInventoryItem(input: {
			sku: "{{.Sku}}", dataTypeID: "{{.DataTypeID}}", data: {{.Data}}
		}) { inventoryItem { id } }
	}`)

	uniqueCreateRepository = resolver.ParseTemplate(`mutation {
		createInventoryRepository(input: {
			name: "{{.Name}}", type: static, dataTypeID: "{{.DataTypeID}}", data: {{.Data}}
		}) { inventoryRepository { id } }
	}`)

	uniqueCreateItemMovement = resolver.ParseTemplate(`mutation {
		createInventoryItemMovement(input: {
			itemID: "{{.ItemID}}", fromID: "{{.FromID}}", toID: "{{.ToID}}",
			handler: "{{.Handler}}", quantity: 1,
			dataTypeID: "{{.DataTypeID}}", data: {{.Data}}
		}) { inventoryItemMovement { id } }
	}`)

	uniqueCreateRepositoryMovement = resolver.ParseTemplate(`mutation {
		createInventoryRepositoryMovement(input: {
			repositoryID: "{{.RepositoryID}}", toID: "{{.ToID}}",
			handler: "{{.Handler}}", executed: false,
			dataTypeID: "{{.DataTypeID}}", data: {{.Data}}
		}) { inventoryRepositoryMovement { id } }
	}`)

	uniqueCreateCollectionMovement = resolver.ParseTemplate(`mutation {
		createInventoryCollectionMovement(input: {
			dataTypeID: "{{.DataTypeID}}", data: {{.Data}}, collection: []
		}) { id }
	}`)

	uniqueCreateItemSet = resolver.ParseTemplate(`mutation {
		createInventoryItemSet(input: {
			sku: "{{.Sku}}", itemIDs: ["{{.ItemID}}"],
			dataTypeID: "{{.DataTypeID}}", data: {{.Data}}
		}) { inventoryItemSet { id } }
	}`)

	uniqueCreateReplenishmentOrder = resolver.ParseTemplate(`mutation {
		createReplenishmentOrder(input: {
			supplierID: "{{.SupplierID}}", dataTypeID: "{{.DataTypeID}}", data: {{.Data}}
		}) { replenishmentOrder { id } }
	}`)

	// The order itself carries no data: only the nested item's unique
	// value is under test.
	uniqueCreateReplenishmentOrderWithItem = resolver.ParseTemplate(`mutation {
		createReplenishmentOrder(input: {
			supplierID: "{{.SupplierID}}",
			items: [{ sku: "{{.Sku}}", quantity: 1, dataTypeID: "{{.DataTypeID}}", data: {{.Data}} }]
		}) { replenishmentOrder { id } }
	}`)

	uniqueUpdateData = resolver.ParseTemplate(`mutation {
		{{.Mutation}}(id: "{{.ID}}", input: { dataTypeID: "{{.DataTypeID}}", data: {{.Data}} }) { __typename }
	}`)

	uniquePatchName = resolver.ParseTemplate(`mutation {
		{{.Mutation}}(id: "{{.ID}}", patches: [{ op: REPLACE, path: "/meta/name", value: "\"{{.Name}}\"" }]) { __typename }
	}`)

	uniqueDelete = resolver.ParseTemplate(`mutation {
		{{.Mutation}}(id: "{{.ID}}") { deletedID }
	}`)
)

// uniqueSoftDeleteCase describes one entity whose data is checked against
// data-type fields marked "unique" and which can be soft-deleted.
type uniqueSoftDeleteCase struct {
	name  string
	table string
	// create runs the entity's create path with meta.name = name under the
	// unique-name data type as userA (tenant A) and returns the new id and
	// any GraphQL errors.
	create func(t *testing.T, te *testEnv, name string) (uuid.UUID, []resolver.GQLError)
	// remove soft-deletes the row through a client-reachable mutation.
	remove func(t *testing.T, te *testEnv, ctx context.Context, id uuid.UUID)
	// updateMutation and patchMutation name the mutations that rewrite the
	// row's data and re-run the uniqueness check.
	updateMutation string
	patchMutation  string
}

// TestUniqueDataField_SoftDeletedRowReleasesValue pins that a unique data
// field ("unique": true in the data type's JSON schema) is enforced among
// live rows only: while a row is live, a second row carrying the same value
// is refused with validator.ErrFieldNotUnique; once that row is soft-deleted,
// the value is free again for create, update and JSON-patch, and a further
// live duplicate is still refused. This matches the platform's partial
// unique indexes (WHERE deleted_at IS NULL) on business keys.
func TestUniqueDataField_SoftDeletedRowReleasesValue(t *testing.T) {
	t.Parallel()

	for _, tc := range uniqueSoftDeleteCases() {
		t.Run(tc.name+"/create", func(t *testing.T) {
			t.Parallel()
			testUniqueCreateAfterSoftDelete(t, tc)
		})

		t.Run(tc.name+"/update", func(t *testing.T) {
			t.Parallel()
			testUniqueRewriteAfterSoftDelete(t, tc, func(te *testEnv, ctx context.Context, id uuid.UUID, name string) []resolver.GQLError {
				return updateUniqueName(te, ctx, tc.updateMutation, id, name)
			})
		})

		t.Run(tc.name+"/patch", func(t *testing.T) {
			t.Parallel()
			testUniqueRewriteAfterSoftDelete(t, tc, func(te *testEnv, ctx context.Context, id uuid.UUID, name string) []resolver.GQLError {
				return patchUniqueName(te, ctx, tc.patchMutation, id, name)
			})
		})
	}
}

// testUniqueRewriteAfterSoftDelete: rewriting a live row's data to a value
// held by another live row is refused, and accepted once that holder is
// soft-deleted.
func testUniqueRewriteAfterSoftDelete(
	t *testing.T,
	tc uniqueSoftDeleteCase,
	rewrite func(te *testEnv, ctx context.Context, id uuid.UUID, name string) []resolver.GQLError,
) {
	t.Helper()
	te := setup(t)
	defer te.Close(t)
	ctx := te.ctx(userA)
	value := "unique-" + uuid.NewString()

	holder := mustCreateUnique(t, te, tc, value)
	other := mustCreateUnique(t, te, tc, "other-"+uuid.NewString())

	requireUniqueViolation(t, rewrite(te, ctx, other, value), "a live row holds the value")

	tc.remove(t, te, ctx, holder)
	requireSoftDeleted(t, te, ctx, tc.table, holder)

	require.Empty(t, rewrite(te, ctx, other, value), "a soft-deleted row must not hold its unique value")
}

// testUniqueCreateAfterSoftDelete: a live duplicate is refused, the value is
// accepted again once its holder is soft-deleted, and the re-created row then
// blocks the next duplicate.
func testUniqueCreateAfterSoftDelete(t *testing.T, tc uniqueSoftDeleteCase) {
	t.Helper()
	te := setup(t)
	defer te.Close(t)
	ctx := te.ctx(userA)
	value := "unique-" + uuid.NewString()

	first := mustCreateUnique(t, te, tc, value)

	_, errs := tc.create(t, te, value)
	requireUniqueViolation(t, errs, "a live row holds the value")

	tc.remove(t, te, ctx, first)
	requireSoftDeleted(t, te, ctx, tc.table, first)

	second, errs := tc.create(t, te, value)
	require.Empty(t, errs, "a soft-deleted row must not hold its unique value")
	require.NotEqual(t, first, second)

	_, errs = tc.create(t, te, value)
	requireUniqueViolation(t, errs, "the re-created live row holds the value again")
}

func uniqueSoftDeleteCases() []uniqueSoftDeleteCase {
	return []uniqueSoftDeleteCase{
		{
			name:  "item",
			table: entitem.Table,
			create: func(t *testing.T, te *testEnv, name string) (uuid.UUID, []resolver.GQLError) {
				t.Helper()
				ctx := te.ctx(userA)
				res := execUnique(te, ctx, uniqueCreateItem, map[string]any{
					"Sku": "SKU-" + uuid.NewString(), "DataTypeID": itemDataTypeIDUniqueName, "Data": uniqueNameData(name),
				})
				return createdID(t, res, "createInventoryItem", "inventoryItem")
			},
			remove:         deleteVia("deleteInventoryItem"),
			updateMutation: "updateInventoryItem",
			patchMutation:  "patchInventoryItemData",
		},
		{
			name:  "repository",
			table: entrepository.Table,
			create: func(t *testing.T, te *testEnv, name string) (uuid.UUID, []resolver.GQLError) {
				t.Helper()
				ctx := te.ctx(userA)
				res := execUnique(te, ctx, uniqueCreateRepository, map[string]any{
					"Name": "repo-" + uuid.NewString(), "DataTypeID": itemDataTypeIDUniqueName, "Data": uniqueNameData(name),
				})
				return createdID(t, res, "createInventoryRepository", "inventoryRepository")
			},
			remove:         deleteVia("deleteInventoryRepository"),
			updateMutation: "updateInventoryRepository",
			patchMutation:  "patchInventoryRepositoryData",
		},
		{
			name:  "itemmovement",
			table: entitemmovement.Table,
			create: func(t *testing.T, te *testEnv, name string) (uuid.UUID, []resolver.GQLError) {
				t.Helper()
				ctx := te.ctx(userA)
				from := te.newRepository(ctx, userA).Create()
				to := te.newRepository(ctx, userA).Create()
				item := te.newItem(ctx, userA).Create()
				te.newStock(ctx, userA, item.ID, from.ID).Quantity(10).Create()
				res := execUnique(te, ctx, uniqueCreateItemMovement, map[string]any{
					"ItemID": item.ID, "FromID": from.ID, "ToID": to.ID, "Handler": testHandler,
					"DataTypeID": itemDataTypeIDUniqueName, "Data": uniqueNameData(name),
				})
				return createdID(t, res, "createInventoryItemMovement", "inventoryItemMovement")
			},
			remove:         deleteVia("deleteInventoryItemMovement"),
			updateMutation: "updateInventoryItemMovement",
			patchMutation:  "patchInventoryItemMovementData",
		},
		{
			name:  "repositorymovement",
			table: entrepositorymovement.Table,
			create: func(t *testing.T, te *testEnv, name string) (uuid.UUID, []resolver.GQLError) {
				t.Helper()
				ctx := te.ctx(userA)
				parent := te.newRepository(ctx, userA).NoData().Create()
				to := te.newRepository(ctx, userA).NoData().Create()
				moving := te.newRepository(ctx, userA).Parent(parent.ID).NoData().Create()
				res := execUnique(te, ctx, uniqueCreateRepositoryMovement, map[string]any{
					"RepositoryID": moving.ID, "ToID": to.ID, "Handler": testHandler,
					"DataTypeID": itemDataTypeIDUniqueName, "Data": uniqueNameData(name),
				})
				return createdID(t, res, "createInventoryRepositoryMovement", "inventoryRepositoryMovement")
			},
			remove:         deleteVia("deleteInventoryRepositoryMovement"),
			updateMutation: "updateInventoryRepositoryMovement",
			patchMutation:  "patchInventoryRepositoryMovementData",
		},
		{
			name:  "collectionmovement",
			table: entcollection_movement.Table,
			create: func(t *testing.T, te *testEnv, name string) (uuid.UUID, []resolver.GQLError) {
				t.Helper()
				ctx := te.ctx(userA)
				res := execUnique(te, ctx, uniqueCreateCollectionMovement, map[string]any{
					"DataTypeID": itemDataTypeIDUniqueName, "Data": uniqueNameData(name),
				})
				return createdID(t, res, "createInventoryCollectionMovement")
			},
			remove:         deleteVia("deleteInventoryCollection"),
			updateMutation: "updateInventoryCollectionMovement",
			patchMutation:  "patchInventoryCollectionMovementData",
		},
		{
			name:  "itemset",
			table: entitemset.Table,
			create: func(t *testing.T, te *testEnv, name string) (uuid.UUID, []resolver.GQLError) {
				t.Helper()
				ctx := te.ctx(userA)
				item := te.newItem(ctx, userA).Create()
				res := execUnique(te, ctx, uniqueCreateItemSet, map[string]any{
					"Sku": "SET-" + uuid.NewString(), "ItemID": item.ID,
					"DataTypeID": itemDataTypeIDUniqueName, "Data": uniqueNameData(name),
				})
				return createdID(t, res, "createInventoryItemSet", "inventoryItemSet")
			},
			remove:         deleteVia("deleteInventoryItemSet"),
			updateMutation: "updateInventoryItemSet",
			patchMutation:  "patchInventoryItemSetData",
		},
		{
			name:  "replenishmentorder",
			table: entreplenishmentorder.Table,
			create: func(t *testing.T, te *testEnv, name string) (uuid.UUID, []resolver.GQLError) {
				t.Helper()
				ctx := te.ctx(userA)
				res := execUnique(te, ctx, uniqueCreateReplenishmentOrder, map[string]any{
					"SupplierID": supplierID, "DataTypeID": itemDataTypeIDUniqueName, "Data": uniqueNameData(name),
				})
				return createdID(t, res, "createReplenishmentOrder", "replenishmentOrder")
			},
			remove:         deleteVia("deleteReplenishmentOrder"),
			updateMutation: "updateReplenishmentOrder",
			patchMutation:  "patchReplenishmentOrderData",
		},
		{
			name:           "replenishmentorderitem",
			table:          entreplenishmentorderitem.Table,
			create:         createReplenishmentOrderItemWithUniqueName,
			remove:         deleteVia("deleteReplenishmentOrderItem"),
			updateMutation: "updateReplenishmentOrderItem",
			patchMutation:  "patchReplenishmentOrderItemData",
		},
		{
			// Same entity, but soft-deleted by the cascade that
			// deleteReplenishmentOrder runs over the order's items.
			name:   "replenishmentorderitem-via-order-delete",
			table:  entreplenishmentorderitem.Table,
			create: createReplenishmentOrderItemWithUniqueName,
			remove: func(t *testing.T, te *testEnv, ctx context.Context, id uuid.UUID) {
				t.Helper()
				orderItem, err := te.Ent.ReplenishmentOrderItem.Get(ctx, id)
				require.NoError(t, err)
				deleteVia("deleteReplenishmentOrder")(t, te, ctx, orderItem.ReplenishmentOrderID)
			},
			updateMutation: "updateReplenishmentOrderItem",
			patchMutation:  "patchReplenishmentOrderItemData",
		},
	}
}

// createReplenishmentOrderItemWithUniqueName creates an order with one item
// carrying the unique value through createReplenishmentOrder's nested items;
// the standalone createReplenishmentOrderItem is covered by
// TestUniqueDataField_CreateReplenishmentOrderItem.
func createReplenishmentOrderItemWithUniqueName(t *testing.T, te *testEnv, name string) (uuid.UUID, []resolver.GQLError) {
	t.Helper()
	ctx := te.ctx(userA)
	res := execUnique(te, ctx, uniqueCreateReplenishmentOrderWithItem, map[string]any{
		"SupplierID": supplierID, "Sku": "RO-" + uuid.NewString(),
		"DataTypeID": itemDataTypeIDUniqueName, "Data": uniqueNameData(name),
	})
	orderID, errs := createdID(t, res, "createReplenishmentOrder", "replenishmentOrder")
	if len(errs) > 0 {
		return uuid.Nil, errs
	}
	orderItem, err := te.Ent.ReplenishmentOrderItem.Query().
		Where(entreplenishmentorderitem.ReplenishmentOrderID(orderID)).
		Only(ctx)
	require.NoError(t, err)
	return orderItem.ID, nil
}

// uniqueNameData renders a GraphQL literal valid against the item_unique_name
// schema, whose meta.name is the unique field.
func uniqueNameData(name string) string {
	return fmt.Sprintf(`{ type: "custom", sum: 15, meta: { name: %q, weight: 50, tags: ["unique"] } }`, name)
}

func execUnique(te *testEnv, ctx context.Context, tpl resolver.Template, args any) resolver.GQLResult[map[string]any] {
	te.t.Helper()
	return resolver.Exec[map[string]any, *ent.Client](te.TestEnvironment, ctx, tpl, args)
}

// createdID returns the id found under path in a successful create response,
// or the GraphQL errors when the create was refused.
func createdID(t *testing.T, res resolver.GQLResult[map[string]any], path ...string) (uuid.UUID, []resolver.GQLError) {
	t.Helper()
	if len(res.Errors) > 0 {
		return uuid.Nil, res.Errors
	}
	var node any = res.Data
	for _, key := range path {
		obj, ok := node.(map[string]any)
		require.True(t, ok, "no object at %q in %v", key, res.Data)
		node = obj[key]
	}
	obj, ok := node.(map[string]any)
	require.True(t, ok, "no object at %v in %v", path, res.Data)
	raw, ok := obj["id"].(string)
	require.True(t, ok, "no id at %v in %v", path, res.Data)
	id, err := uuid.Parse(raw)
	require.NoError(t, err)
	return id, nil
}

func mustCreateUnique(t *testing.T, te *testEnv, tc uniqueSoftDeleteCase, name string) uuid.UUID {
	t.Helper()
	id, errs := tc.create(t, te, name)
	require.Empty(t, errs, "fixture create failed")
	return id
}

// deleteVia returns a remover that calls the named delete mutation.
func deleteVia(mutation string) func(t *testing.T, te *testEnv, ctx context.Context, id uuid.UUID) {
	return func(t *testing.T, te *testEnv, ctx context.Context, id uuid.UUID) {
		t.Helper()
		res := execUnique(te, ctx, uniqueDelete, map[string]any{"Mutation": mutation, "ID": id})
		require.Empty(t, res.Errors, "%s failed", mutation)
	}
}

func updateUniqueName(te *testEnv, ctx context.Context, mutation string, id uuid.UUID, name string) []resolver.GQLError {
	te.t.Helper()
	return execUnique(te, ctx, uniqueUpdateData, map[string]any{
		"Mutation": mutation, "ID": id, "DataTypeID": itemDataTypeIDUniqueName, "Data": uniqueNameData(name),
	}).Errors
}

func patchUniqueName(te *testEnv, ctx context.Context, mutation string, id uuid.UUID, name string) []resolver.GQLError {
	te.t.Helper()
	return execUnique(te, ctx, uniquePatchName, map[string]any{"Mutation": mutation, "ID": id, "Name": name}).Errors
}

func requireUniqueViolation(t *testing.T, errs []resolver.GQLError, why string) {
	t.Helper()
	require.NotEmpty(t, errs, "duplicate accepted although %s", why)
	require.Contains(t, errs[0].Message, validator.ErrFieldNotUnique.Error(), "duplicate refused for another reason although %s", why)
}

// requireSoftDeleted proves the delete kept the row and stamped deleted_at,
// so the re-use assertions exercise a soft-deleted row, not a missing one.
func requireSoftDeleted(t *testing.T, te *testEnv, ctx context.Context, table string, id uuid.UUID) {
	t.Helper()
	rows, err := te.Ent.QueryContext(ctx, fmt.Sprintf(`SELECT deleted_at IS NOT NULL FROM %q WHERE id = $1`, table), id)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	require.True(t, rows.Next(), "row %s is gone from %s: the delete is hard, not soft", id, table)
	var deleted bool
	require.NoError(t, rows.Scan(&deleted))
	require.True(t, deleted, "row %s in %s has no deleted_at after its delete", id, table)
	require.NoError(t, rows.Err())
}
