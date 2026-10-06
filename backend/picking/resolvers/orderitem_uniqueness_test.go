package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/test"
	"github.com/pyck-ai/pyck/backend/common/validator"
)

// uniqueNameDataTypeID declares meta.name unique (item_unique_name schema).
var uniqueNameDataTypeID = uuid.MustParse("0192f5c4-7a1e-7c3b-9d2e-5b6a7c8d9e02")

func (te *testEnv) addUniqueNameDataType(t *testing.T) {
	t.Helper()
	schema, err := test.LoadSchemaByName("item_unique_name")
	require.NoError(t, err)
	te.DataTypeProvider.AddDataType(json_schema.DataType{
		ID:         uniqueNameDataTypeID,
		Slug:       "item_unique_name",
		TenantID:   tenantA,
		JsonSchema: string(schema),
	})
}

// TestCreateOrderItemChecksUniqueness pins that createPickingOrderItem runs
// the uniqueness check for a data type with a unique field: the check must
// reach the hyphenated order-items table instead of failing on its name.
func TestCreateOrderItemChecksUniqueness(t *testing.T) {
	t.Parallel()

	te := setup(t)
	te.addUniqueNameDataType(t)

	ctx := te.ctx(userA)
	order := te.newOrder(ctx, userA).Create()

	execOK[createOrderItemData](te, ctx, createOrderItem, map[string]any{
		"Sku": "SKU-1", "Quantity": 1, "OrderID": order.ID, "DataTypeID": uniqueNameDataTypeID, "Name": "first",
	})
	execErr(te, ctx, createOrderItem, map[string]any{
		"Sku": "SKU-2", "Quantity": 1, "OrderID": order.ID, "DataTypeID": uniqueNameDataTypeID, "Name": "first",
	}, validator.ErrFieldNotUnique.Error())
}

// TestHyphenatedPickingTablesCheckUniqueness pins the other writes on
// picking's hyphenated tables: updating an order item, and creating and
// updating an outbound shipment notification, each check a unique data field
// against their own table, refusing a taken value and accepting a new one.
func TestHyphenatedPickingTablesCheckUniqueness(t *testing.T) {
	t.Parallel()

	t.Run("update order item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		te.addUniqueNameDataType(t)
		ctx := te.ctx(userA)
		order := te.newOrder(ctx, userA).Create()
		execOK[createOrderItemData](te, ctx, createOrderItem, map[string]any{
			"Sku": "SKU-1", "Quantity": 1, "OrderID": order.ID, "DataTypeID": uniqueNameDataTypeID, "Name": "first",
		})
		second := execOK[createOrderItemData](te, ctx, createOrderItem, map[string]any{
			"Sku": "SKU-2", "Quantity": 1, "OrderID": order.ID, "DataTypeID": uniqueNameDataTypeID, "Name": "second",
		}).CreatePickingOrderItem.PickingOrderItem.ID

		execErr(te, ctx, updateOrderItem, map[string]any{
			"ID": second, "DataTypeID": uniqueNameDataTypeID, "Data": true, "Name": "first",
		}, validator.ErrFieldNotUnique.Error())
		execOK[updateOrderItemData](te, ctx, updateOrderItem, map[string]any{
			"ID": second, "DataTypeID": uniqueNameDataTypeID, "Data": true, "Name": "renamed",
		})
	})

	t.Run("create and update outbound shipment notification", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		te.addUniqueNameDataType(t)
		ctx := te.ctx(userA)
		order := te.newOrder(ctx, userA).Create()
		execOK[createNotificationData](te, ctx, createNotification, map[string]any{
			"OrderID": order.ID, "DataTypeID": uniqueNameDataTypeID, "Name": "first",
		})
		execErr(te, ctx, createNotification, map[string]any{
			"OrderID": order.ID, "DataTypeID": uniqueNameDataTypeID, "Name": "first",
		}, validator.ErrFieldNotUnique.Error())
		second := execOK[createNotificationData](te, ctx, createNotification, map[string]any{
			"OrderID": order.ID, "DataTypeID": uniqueNameDataTypeID, "Name": "second",
		}).CreatePickingOutboundShipmentNotification.PickingOutboundShipmentNotification.ID

		execErr(te, ctx, updateNotification, map[string]any{
			"ID": second, "DataTypeID": uniqueNameDataTypeID, "Data": true, "Name": "first",
		}, validator.ErrFieldNotUnique.Error())
		execOK[updateNotificationData](te, ctx, updateNotification, map[string]any{
			"ID": second, "DataTypeID": uniqueNameDataTypeID, "Data": true, "Name": "renamed",
		})
	})
}
