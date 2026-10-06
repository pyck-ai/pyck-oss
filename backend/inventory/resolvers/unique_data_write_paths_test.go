package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	entreplenishmentorderitem "github.com/pyck-ai/pyck/backend/inventory/ent/gen/replenishmentorderitem"
)

var uniqueCreateReplenishmentOrderItem = resolver.ParseTemplate(`mutation {
	createReplenishmentOrderItem(input: {
		replenishmentorderID: "{{.OrderID}}", sku: "{{.Sku}}", quantity: 1,
		dataTypeID: "{{.DataTypeID}}", data: {{.Data}}
	}) { replenishmentOrderItem { id } }
}`)

// TestUniqueDataField_CreateCollectionMovement pins that
// createInventoryCollectionMovement enforces a unique data field against the
// other live collection movements only: the first collection carrying a value
// is accepted (the new row does not count against itself), a second one with
// the same value is refused with validator.ErrFieldNotUnique, and a different
// value is accepted.
func TestUniqueDataField_CreateCollectionMovement(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)
	value := "unique-" + uuid.NewString()

	createCollection := func(name string) (uuid.UUID, []resolver.GQLError) {
		t.Helper()
		res := execUnique(te, te.ctx(userA), uniqueCreateCollectionMovement, map[string]any{
			"DataTypeID": itemDataTypeIDUniqueName, "Data": uniqueNameData(name),
		})
		return createdID(t, res, "createInventoryCollectionMovement")
	}

	first, errs := createCollection(value)
	require.Empty(t, errs, "the first collection carrying the value must be accepted")
	require.NotEqual(t, uuid.Nil, first)

	_, errs = createCollection(value)
	requireUniqueViolation(t, errs, "a live collection movement holds the value")

	_, errs = createCollection("other-" + uuid.NewString())
	require.Empty(t, errs, "a value no live collection movement holds must be accepted")
}

// TestUniqueDataField_CreateReplenishmentOrderItem pins that the standalone
// createReplenishmentOrderItem enforces a unique data field like the items
// created inside createReplenishmentOrder: a duplicate of a live item's value
// is refused with validator.ErrFieldNotUnique, a new value is accepted, and a
// value held only by a soft-deleted item is free again.
func TestUniqueDataField_CreateReplenishmentOrderItem(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)
	ctx := te.ctx(userA)
	order := te.newReplenishmentOrder(ctx, userA).Create()
	value := "unique-" + uuid.NewString()

	createItem := func(name string) (uuid.UUID, []resolver.GQLError) {
		t.Helper()
		res := execUnique(te, ctx, uniqueCreateReplenishmentOrderItem, map[string]any{
			"OrderID": order.ID, "Sku": "RO-" + uuid.NewString(),
			"DataTypeID": itemDataTypeIDUniqueName, "Data": uniqueNameData(name),
		})
		return createdID(t, res, "createReplenishmentOrderItem", "replenishmentOrderItem")
	}

	holder, errs := createItem(value)
	require.Empty(t, errs, "the first item carrying the value must be accepted")

	_, errs = createItem(value)
	requireUniqueViolation(t, errs, "a live order item holds the value")

	_, errs = createItem("other-" + uuid.NewString())
	require.Empty(t, errs, "a value no live order item holds must be accepted")

	deleteVia("deleteReplenishmentOrderItem")(t, te, ctx, holder)
	requireSoftDeleted(t, te, ctx, entreplenishmentorderitem.Table, holder)

	_, errs = createItem(value)
	require.Empty(t, errs, "a soft-deleted order item must not hold its unique value")
}
