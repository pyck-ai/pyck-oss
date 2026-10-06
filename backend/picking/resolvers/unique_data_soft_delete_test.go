package resolvers_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/validator"
)

// createOrderWithItem creates an order with one inline order item; the order
// and the item each take their own data type and meta.name.
var createOrderWithItem = resolver.ParseTemplate(`mutation {
	createPickingOrder(input: {
		dataTypeID: "{{.OrderDataTypeID}}",
		data: { type: "custom", sum: 15, meta: { name: "{{.OrderName}}", weight: 50, tags: ["a"] } },
		orderItems: [{
			sku: "{{.Sku}}",
			quantity: 1,
			dataTypeID: "{{.DataTypeID}}",
			data: { type: "custom", sum: 15, meta: { name: "{{.ItemName}}", weight: 50, tags: ["a"] } }
		}]
	}) {
		pickingOrder { id }
	}
}`)

// takenName is the unique meta.name value a deleted record held.
const takenName = "taken"

// softDeleteTarget is one picking entity written with the unique-name data
// type: its create, update, patch and delete mutations.
type softDeleteTarget struct {
	create     resolver.Template
	createArgs func(name string) map[string]any
	update     resolver.Template
	patch      resolver.Template
	del        resolver.Template
}

// TestSoftDeletedRecordReleasesUniqueValue pins that a soft-deleted order,
// order item or outbound shipment notification no longer holds its unique
// data value: while the record is live a second write carrying the value is
// refused, and once its delete mutation has soft-deleted it, creating,
// updating or patching another record to that value is accepted — the same
// rule as the partial unique indexes (WHERE deleted_at IS NULL).
func TestSoftDeletedRecordReleasesUniqueValue(t *testing.T) {
	t.Parallel()

	targets := map[string]func(te *testEnv) softDeleteTarget{
		"order": func(*testEnv) softDeleteTarget {
			return softDeleteTarget{
				create: createOrder,
				createArgs: func(name string) map[string]any {
					return map[string]any{"DataTypeID": uniqueNameDataTypeID, "Name": name}
				},
				update: updateOrder,
				patch:  patchOrderData,
				del:    deleteOrder,
			}
		},
		"order item": func(te *testEnv) softDeleteTarget {
			parent := te.newOrder(te.ctx(userA), userA).Create()
			return softDeleteTarget{
				create: createOrderItem,
				createArgs: func(name string) map[string]any {
					return map[string]any{
						"OrderID": parent.ID, "Sku": "SKU-" + uuid.NewString(), "Quantity": 1, "DataTypeID": uniqueNameDataTypeID, "Name": name,
					}
				},
				update: updateOrderItem,
				patch:  patchOrderItemData,
				del:    deleteOrderItem,
			}
		},
		"outbound shipment notification": func(te *testEnv) softDeleteTarget {
			parent := te.newOrder(te.ctx(userA), userA).Create()
			return softDeleteTarget{
				create: createNotification,
				createArgs: func(name string) map[string]any {
					return map[string]any{"OrderID": parent.ID, "DataTypeID": uniqueNameDataTypeID, "Name": name}
				},
				update: updateNotification,
				patch:  patchNotificationData,
				del:    deleteNotification,
			}
		},
	}

	for entity, newTarget := range targets {
		t.Run(entity, func(t *testing.T) {
			t.Parallel()

			t.Run("create after delete", func(t *testing.T) {
				t.Parallel()
				te, ctx := setupUnique(t)
				tg := newTarget(te)
				first := writtenID(t, execOK[json.RawMessage](te, ctx, tg.create, tg.createArgs(takenName)))

				node := te.requireReleasedByDelete(ctx, tg.create, tg.createArgs(takenName), func() {
					te.deleteByMutation(ctx, tg.del, first)
				})

				require.NotEqual(t, first.String(), node["id"])
				requireMetaName(t, node)
			})

			t.Run("update after delete", func(t *testing.T) {
				t.Parallel()
				te, ctx := setupUnique(t)
				tg := newTarget(te)
				first := writtenID(t, execOK[json.RawMessage](te, ctx, tg.create, tg.createArgs(takenName)))
				second := writtenID(t, execOK[json.RawMessage](te, ctx, tg.create, tg.createArgs("other")))

				node := te.requireReleasedByDelete(ctx, tg.update, map[string]any{
					"ID": second, "DataTypeID": uniqueNameDataTypeID, "Data": true, "Name": takenName,
				}, func() {
					te.deleteByMutation(ctx, tg.del, first)
				})

				require.Equal(t, second.String(), node["id"])
				requireMetaName(t, node)
			})

			t.Run("patch after delete", func(t *testing.T) {
				t.Parallel()
				te, ctx := setupUnique(t)
				tg := newTarget(te)
				first := writtenID(t, execOK[json.RawMessage](te, ctx, tg.create, tg.createArgs(takenName)))
				second := writtenID(t, execOK[json.RawMessage](te, ctx, tg.create, tg.createArgs("other")))

				node := te.requireReleasedByDelete(ctx, tg.patch, map[string]any{
					"ID":      second,
					"Patches": []patch{{Op: "REPLACE", Path: "/meta/name", Value: `\"` + takenName + `\"`}},
				}, func() {
					te.deleteByMutation(ctx, tg.del, first)
				})

				require.Equal(t, second.String(), node["id"])
				requireMetaName(t, node)
			})
		})
	}
}

// TestDeletedOrderReleasesItsItemsUniqueValues pins that deletePickingOrder,
// which soft-deletes the order together with its items, releases the unique
// values those items held: re-creating the same order with the same inline
// items, adding an item with the value to another order, and updating another
// order's item to the value are all accepted afterwards.
func TestDeletedOrderReleasesItsItemsUniqueValues(t *testing.T) {
	t.Parallel()

	t.Run("re-create the order with the same inline items", func(t *testing.T) {
		t.Parallel()
		te, ctx := setupUnique(t)
		args := map[string]any{
			"OrderDataTypeID": uniqueNameDataTypeID,
			"OrderName":       "order-taken",
			"Sku":             "SKU-1",
			"DataTypeID":      uniqueNameDataTypeID,
			"ItemName":        "item-taken",
		}
		original := writtenID(t, execOK[json.RawMessage](te, ctx, createOrderWithItem, args))

		// The live item alone refuses the value, independent of the order's own name.
		execErr(te, ctx, createOrderWithItem, map[string]any{
			"OrderDataTypeID": uniqueNameDataTypeID,
			"OrderName":       "order-fresh",
			"Sku":             "SKU-2",
			"DataTypeID":      uniqueNameDataTypeID,
			"ItemName":        "item-taken",
		}, validator.ErrFieldNotUnique.Error())

		node := te.requireReleasedByDelete(ctx, createOrderWithItem, args, func() {
			te.deleteByMutation(ctx, deleteOrder, original)
		})

		require.NotEqual(t, original.String(), node["id"])
		live, err := te.Ent.OrderItems.Query().Count(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, live, "only the re-created inline item is live")
	})

	t.Run("create an order whose inline item reuses the value", func(t *testing.T) {
		t.Parallel()
		te, ctx := setupUnique(t)
		// The order's own data type has no unique field, so only the inline
		// item check can refuse the write.
		args := map[string]any{
			"OrderDataTypeID": itemDataTypeID, "OrderName": "order", "Sku": "SKU-1",
			"DataTypeID": uniqueNameDataTypeID, "ItemName": takenName,
		}
		original := writtenID(t, execOK[json.RawMessage](te, ctx, createOrderWithItem, args))

		node := te.requireReleasedByDelete(ctx, createOrderWithItem, args, func() {
			te.deleteByMutation(ctx, deleteOrder, original)
		})

		require.NotEqual(t, original.String(), node["id"])
	})

	t.Run("create an item on another order", func(t *testing.T) {
		t.Parallel()
		te, ctx := setupUnique(t)
		parent := writtenID(t, execOK[json.RawMessage](te, ctx, createOrderWithItem, map[string]any{
			"OrderDataTypeID": itemDataTypeID, "OrderName": "parent", "Sku": "SKU-1",
			"DataTypeID": uniqueNameDataTypeID, "ItemName": takenName,
		}))
		other := te.newOrder(ctx, userA).Create()

		node := te.requireReleasedByDelete(ctx, createOrderItem, map[string]any{
			"OrderID": other.ID, "Sku": "SKU-2", "Quantity": 1, "DataTypeID": uniqueNameDataTypeID, "Name": takenName,
		}, func() {
			te.deleteByMutation(ctx, deleteOrder, parent)
		})

		require.Equal(t, other.ID.String(), node["orderID"])
		requireMetaName(t, node)
	})

	t.Run("update an item on another order", func(t *testing.T) {
		t.Parallel()
		te, ctx := setupUnique(t)
		parent := writtenID(t, execOK[json.RawMessage](te, ctx, createOrderWithItem, map[string]any{
			"OrderDataTypeID": itemDataTypeID, "OrderName": "parent", "Sku": "SKU-1",
			"DataTypeID": uniqueNameDataTypeID, "ItemName": takenName,
		}))
		other := te.newOrder(ctx, userA).Create()
		item := writtenID(t, execOK[json.RawMessage](te, ctx, createOrderItem, map[string]any{
			"OrderID": other.ID, "Sku": "SKU-2", "Quantity": 1, "DataTypeID": uniqueNameDataTypeID, "Name": "other",
		}))

		node := te.requireReleasedByDelete(ctx, updateOrderItem, map[string]any{
			"ID": item, "DataTypeID": uniqueNameDataTypeID, "Data": true, "Name": takenName,
		}, func() {
			te.deleteByMutation(ctx, deleteOrder, parent)
		})

		require.Equal(t, item.String(), node["id"])
		requireMetaName(t, node)
	})
}

// setupUnique returns a test environment that knows the unique-name data
// type, and tenant A's admin context.
func setupUnique(t *testing.T) (*testEnv, context.Context) {
	t.Helper()
	te := setup(t)
	te.addUniqueNameDataType(t)
	return te, te.ctx(userA)
}

// requireReleasedByDelete sends write while the record holding its unique
// value is live and requires the unique-violation refusal, runs del, then
// sends the identical write again and requires it to succeed. It returns the
// node the accepted write answered with.
func (te *testEnv) requireReleasedByDelete(ctx context.Context, write resolver.Template, args map[string]any, del func()) map[string]any {
	te.t.Helper()
	execErr(te, ctx, write, args, validator.ErrFieldNotUnique.Error())
	del()
	return writtenNode(te.t, execOK[json.RawMessage](te, ctx, write, args))
}

// deleteByMutation runs a delete mutation for id and requires it to report
// that id as deleted.
func (te *testEnv) deleteByMutation(ctx context.Context, del resolver.Template, id uuid.UUID) {
	te.t.Helper()
	var data map[string]map[string]any
	require.NoError(te.t, json.Unmarshal(execOK[json.RawMessage](te, ctx, del, map[string]any{"ID": id}), &data))
	require.Len(te.t, data, 1)
	for _, payload := range data {
		require.Equal(te.t, id.String(), payload["deletedID"])
	}
}

// writtenNode returns the entity node of a single create, update or patch
// mutation response ({mutation: {entity: node}}).
func writtenNode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var data map[string]map[string]map[string]any
	require.NoError(t, json.Unmarshal(raw, &data))
	require.Len(t, data, 1)
	for _, payload := range data {
		require.Len(t, payload, 1)
		for _, node := range payload {
			return node
		}
	}
	return nil
}

// writtenID returns the id of the entity a single write mutation answered with.
func writtenID(t *testing.T, raw json.RawMessage) uuid.UUID {
	t.Helper()
	id, ok := writtenNode(t, raw)["id"].(string)
	require.True(t, ok, "written node has no id")
	return uuid.MustParse(id)
}

// requireMetaName requires node's data to carry takenName as meta.name.
func requireMetaName(t *testing.T, node map[string]any) {
	t.Helper()
	data, ok := node["data"].(map[string]any)
	require.True(t, ok, "node has no data object")
	meta, ok := data["meta"].(map[string]any)
	require.True(t, ok, "data has no meta object")
	require.Equal(t, takenName, meta["name"])
}
