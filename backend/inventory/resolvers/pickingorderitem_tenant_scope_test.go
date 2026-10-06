package resolvers_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// entitiesPickingOrderItem is the representation the router sends to resolve
// a picking order item's inventory fields: the item's sku and its tenantID.
var entitiesPickingOrderItem = resolver.ParseTemplate(`query {
	_entities(representations: [{__typename: "PickingOrderItem", sku: "{{.Sku}}", tenantID: "{{.TenantID}}"}]) {
		... on PickingOrderItem { sku item { id tenantID } availableStock reservedStock }
	}
}`)

type entitiesPickingOrderItemData struct {
	Entities []struct {
		Sku  string `json:"sku"`
		Item *struct {
			ID       string `json:"id"`
			TenantID string `json:"tenantID"`
		} `json:"item"`
		AvailableStock *int64 `json:"availableStock"`
		ReservedStock  *int64 `json:"reservedStock"`
	} `json:"_entities"`
}

// TestPickingOrderItemStaysInItemTenant pins that a picking order item's
// inventory item and stock come from the order item's own tenant. A SKU is
// unique per tenant only, so two tenants routinely hold the same SKU. A
// reader limited to one tenant only ever saw that tenant's item, but the
// system user skips the tenant filter and a reader acting in A and B sees
// both tenants, so the lookup by SKU alone returned whichever tenant's item
// and warehouse came first.
func TestPickingOrderItemStaysInItemTenant(t *testing.T) {
	t.Parallel()

	const sku = "shared-sku"

	systemUser := &authn.User{ID: uuid.Max, TenantID: uuid.Max}
	bothUser := &authn.User{
		ID:       uuid.New(),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_READER, tenantB: authn.ROLE_READER},
	}

	te := setup(t)

	// B's rows are created first, so an unscoped First() finds them first.
	ctxB := te.ctx(userB)
	warehouseB := te.newRepository(ctxB, userB).Name("warehouse-b").DataTypeID(itemDataTypeIDTenantB).Create()
	itemB := te.newItem(ctxB, userB).Sku(sku).DataType(itemDataTypeIDTenantB, itemDataTypeSlug).Create()
	te.newStock(ctxB, userB, itemB.ID, warehouseB.ID).Quantity(50).Outgoing(5).Create()

	ctxA := te.ctx(userA)
	warehouseA := te.newRepository(ctxA, userA).Name("warehouse-a").Create()
	itemA := te.newItem(ctxA, userA).Sku(sku).Create()
	te.newStock(ctxA, userA, itemA.ID, warehouseA.ID).Quantity(7).Outgoing(2).Create()

	type reader struct {
		name string
		ctx  context.Context //nolint:containedctx // one request context per reader
	}
	spanning := []reader{
		{"system user", request.Context(te.ctx(userA), systemUser)},
		{"user acting in A and B", request.Context(te.ctx(userA), bothUser, tenantA, tenantB)},
	}

	owners := []struct {
		label     string
		tenant    uuid.UUID
		itemID    uuid.UUID
		available int64
		reserved  int64
		own       reader
	}{
		{"A", tenantA, itemA.ID, 7, 2, reader{"single-tenant user of A", ctxA}},
		{"B", tenantB, itemB.ID, 50, 5, reader{"single-tenant user of B", ctxB}},
	}

	for _, owner := range owners {
		for _, rd := range append([]reader{owner.own}, spanning...) {
			t.Run(owner.label+"'s order item/"+rd.name, func(t *testing.T) {
				t.Parallel()

				data := execOK[entitiesPickingOrderItemData](te, rd.ctx, entitiesPickingOrderItem,
					map[string]any{"Sku": sku, "TenantID": owner.tenant})
				require.Len(t, data.Entities, 1)
				got := data.Entities[0]

				require.NotNil(t, got.Item, "order item of tenant %s resolved no item", owner.label)
				assert.Equal(t, owner.itemID.String(), got.Item.ID,
					"order item of tenant %s resolved the item of tenant %s", owner.label, got.Item.TenantID)
				assert.Equal(t, owner.tenant.String(), got.Item.TenantID)
				require.NotNil(t, got.AvailableStock)
				require.NotNil(t, got.ReservedStock)
				assert.Equal(t, owner.available, *got.AvailableStock,
					"order item of tenant %s shows another tenant's available stock", owner.label)
				assert.Equal(t, owner.reserved, *got.ReservedStock,
					"order item of tenant %s shows another tenant's reserved stock", owner.label)
			})
		}
	}
}
