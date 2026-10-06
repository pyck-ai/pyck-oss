package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/test"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/txid"
	"github.com/pyck-ai/pyck/backend/common/validator"

	ent "github.com/pyck-ai/pyck/backend/receiving/ent/gen"
)

// uniqueNameDataTypeID declares meta.name unique (item_unique_name schema).
var uniqueNameDataTypeID = uuid.MustParse("0192f5c4-7a1e-7c3b-9d2e-5b6a7c8d9e01")

var createInboundWithItem = resolver.ParseTemplate(`mutation {
	createReceivingInbound(input: {
		dataTypeID: "{{.InboundDataTypeID}}",
		data: { type: "custom", sum: 15, meta: { name: "{{.InboundName}}", weight: 50, tags: ["a"] } },
		inboundItems: [{
			sku: "{{.Sku}}",
			quantity: 1,
			dataTypeID: "{{.DataTypeID}}",
			data: { type: "custom", sum: 15, meta: { name: "{{.ItemName}}", weight: 50, tags: ["a"] } }
		}]
	}) {
		receivingInbound { id }
	}
}`)

func (te *testEnv) addUniqueNameDataType() {
	te.t.Helper()
	schema, err := test.LoadSchemaByName("item_unique_name")
	require.NoError(te.t, err)
	te.DataTypeProvider.AddDataType(json_schema.DataType{
		ID:         uniqueNameDataTypeID,
		Slug:       "item_unique_name",
		TenantID:   tenantA,
		JsonSchema: string(schema),
	})
}

// storeInbound writes an inbound of the unique-name data type directly, so the
// test controls which table the name lands in.
func (te *testEnv) storeInbound(name string) *ent.Inbound {
	te.t.Helper()
	ctx := te.ctx(userA)
	var in *ent.Inbound
	require.NoError(te.t, te.withTx(ctx, func(tx *ent.Tx) error {
		var err error
		in, err = tx.Inbound.Create().
			SetTenantID(tenantA).
			SetDataTypeID(uniqueNameDataTypeID).
			SetData(map[string]any{"type": "custom", "sum": 15, "meta": map[string]any{"name": name, "weight": 50, "tags": []any{"a"}}}).
			Save(ent.NewTxContext(txid.With(ctx, txid.New()), tx))
		return err
	}))
	return in
}

// storeItem writes an inbound item of the unique-name data type directly.
func (te *testEnv) storeItem(inboundID uuid.UUID, name string) {
	te.t.Helper()
	ctx := te.ctx(userA)
	require.NoError(te.t, te.withTx(ctx, func(tx *ent.Tx) error {
		_, err := tx.InboundItem.Create().
			SetTenantID(tenantA).
			SetInboundID(inboundID).
			SetSku("SKU-" + name).
			SetQuantity(1).
			SetDataTypeID(uniqueNameDataTypeID).
			SetData(map[string]any{"type": "custom", "sum": 15, "meta": map[string]any{"name": name, "weight": 50, "tags": []any{"a"}}}).
			Save(ent.NewTxContext(txid.With(ctx, txid.New()), tx))
		return err
	}))
}

// TestCreateInboundChecksItemUniquenessAgainstItems pins that the items
// nested in createReceivingInbound are checked for unique data fields against
// the inbound-item table, as createReceivingInboundItem already does.
func TestCreateInboundChecksItemUniquenessAgainstItems(t *testing.T) {
	t.Parallel()

	t.Run("refuses an item that duplicates an existing item", func(t *testing.T) {
		t.Parallel()

		te := setup(t)
		te.addUniqueNameDataType()
		existing := te.storeInbound("inbound-1")
		te.storeItem(existing.ID, "dup-item")

		execErr(te, te.ctx(userA), createInboundWithItem, map[string]any{
			"InboundDataTypeID": itemDataTypeID,
			"InboundName":       "inbound-2",
			"Sku":               "SKU-2",
			"DataTypeID":        uniqueNameDataTypeID,
			"ItemName":          "dup-item",
		}, validator.ErrFieldNotUnique.Error())

		// The refused request must not leave a second item behind.
		n, err := te.Ent.InboundItem.Query().Count(te.ctx(userA))
		require.NoError(t, err)
		require.Equal(t, 1, n)
	})

	t.Run("accepts an item whose value only matches an inbound", func(t *testing.T) {
		t.Parallel()

		te := setup(t)
		te.addUniqueNameDataType()
		te.storeInbound("shared-name")

		execOK[createInboundData](te, te.ctx(userA), createInboundWithItem, map[string]any{
			"InboundDataTypeID": itemDataTypeID,
			"InboundName":       "inbound-2",
			"Sku":               "SKU-2",
			"DataTypeID":        uniqueNameDataTypeID,
			"ItemName":          "shared-name",
		})
	})
}

// TestCreateInboundItemChecksUniqueness pins that createReceivingInboundItem
// runs the uniqueness check at all: the check must reach the hyphenated
// inbound-items table instead of failing on its name.
func TestCreateInboundItemChecksUniqueness(t *testing.T) {
	t.Parallel()

	te := setup(t)
	te.addUniqueNameDataType()
	in := te.storeInbound("inbound-1")

	execOK[createItemData](te, te.ctx(userA), createItem, map[string]any{
		"InboundID": in.ID, "Sku": "SKU-1", "DataTypeID": uniqueNameDataTypeID, "Name": "first",
	})
	execErr(te, te.ctx(userA), createItem, map[string]any{
		"InboundID": in.ID, "Sku": "SKU-2", "DataTypeID": uniqueNameDataTypeID, "Name": "first",
	}, validator.ErrFieldNotUnique.Error())
}

// TestHyphenatedReceivingTablesCheckUniqueness pins the other writes on
// receiving's hyphenated tables: updating an inbound item, and creating and
// updating an inbound shipment notification, each check a unique data field
// against their own table, refusing a taken value and accepting a new one.
func TestHyphenatedReceivingTablesCheckUniqueness(t *testing.T) {
	t.Parallel()

	t.Run("update inbound item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		te.addUniqueNameDataType()
		ctx := te.ctx(userA)
		in := te.storeInbound("inbound-1")
		execOK[createItemData](te, ctx, createItem, map[string]any{
			"InboundID": in.ID, "Sku": "SKU-1", "DataTypeID": uniqueNameDataTypeID, "Name": "first",
		})
		second := execOK[createItemData](te, ctx, createItem, map[string]any{
			"InboundID": in.ID, "Sku": "SKU-2", "DataTypeID": uniqueNameDataTypeID, "Name": "second",
		}).CreateReceivingInboundItem.ReceivingInboundItem.ID

		execErr(te, ctx, updateItem, map[string]any{
			"ID": second, "DataTypeID": uniqueNameDataTypeID, "Data": true, "Name": "first",
		}, validator.ErrFieldNotUnique.Error())
		execOK[updateItemData](te, ctx, updateItem, map[string]any{
			"ID": second, "DataTypeID": uniqueNameDataTypeID, "Data": true, "Name": "renamed",
		})
	})

	t.Run("create and update inbound shipment notification", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		te.addUniqueNameDataType()
		ctx := te.ctx(userA)
		in := te.storeInbound("inbound-1")
		execOK[createNotificationData](te, ctx, createNotification, map[string]any{
			"InboundID": in.ID, "DataTypeID": uniqueNameDataTypeID, "Name": "first",
		})
		execErr(te, ctx, createNotification, map[string]any{
			"InboundID": in.ID, "DataTypeID": uniqueNameDataTypeID, "Name": "first",
		}, validator.ErrFieldNotUnique.Error())
		second := execOK[createNotificationData](te, ctx, createNotification, map[string]any{
			"InboundID": in.ID, "DataTypeID": uniqueNameDataTypeID, "Name": "second",
		}).CreateReceivingInboundShipmentNotification.ReceivingInboundShipmentNotification.ID

		execErr(te, ctx, updateNotification, map[string]any{
			"ID": second, "DataTypeID": uniqueNameDataTypeID, "Data": true, "Name": "first",
		}, validator.ErrFieldNotUnique.Error())
		execOK[updateNotificationData](te, ctx, updateNotification, map[string]any{
			"ID": second, "DataTypeID": uniqueNameDataTypeID, "Data": true, "Name": "renamed",
		})
	})
}
