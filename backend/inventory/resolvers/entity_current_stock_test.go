package resolvers_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/validator"

	"github.com/pyck-ai/pyck/backend/inventory/resolvers"
)

// Pins the current-row rule on the read the gateway serves: highest version,
// not latest created_at, which under pod clock skew served a superseded row.
func TestFindPickingOrderItemBySku_PicksHighestVersion(t *testing.T) {
	t.Parallel()

	te := setup(t)
	ctx := te.ctx(userA)

	// The resolver looks the warehouse up itself, so the fixture must be the
	// only non-virtual root.
	warehouse := te.newRepository(ctx, userA).Name("federation-warehouse").Create()
	item := te.newItem(ctx, userA).Sku("federation-sku").Create()

	te.newStock(ctx, userA, item.ID, warehouse.ID).Quantity(100).Outgoing(9).Create()
	current := te.newStock(ctx, userA, item.ID, warehouse.ID).Quantity(7).Outgoing(0).Create()

	// Invert the clock against the versions: the newer row carries the older
	// timestamp, which is what a multi-pod deployment produces under skew.
	execSQL(t, ctx, te.Ent,
		"UPDATE stocks SET created_at = created_at - interval '1 hour' WHERE id = ?", current.ID)

	entity := resolvers.NewResolver(
		"inventory", te.Ent, validator.NewValidator(te.DataTypeProvider), te.StockService,
	).Entity()

	got, err := entity.FindPickingOrderItemBySkuAndTenantID(ctx, "federation-sku", tenantA)
	require.NoError(t, err)
	require.NotNil(t, got)

	require.Equal(t, int64(7), *got.AvailableStock,
		"a superseded row must not be served as the SKU's available stock")
	require.Equal(t, int64(0), *got.ReservedStock,
		"a superseded row must not be served as the SKU's reserved stock")
}

// Pins that an item with no stock row in the warehouse reads as zero stock,
// not as an error: the stock lookup's not-found result is the empty state.
func TestFindPickingOrderItemBySku_NoStockRowIsZero(t *testing.T) {
	t.Parallel()

	te := setup(t)
	ctx := te.ctx(userA)

	te.newRepository(ctx, userA).Name("federation-warehouse").Create()
	te.newItem(ctx, userA).Sku("unstocked-sku").Create()

	entity := resolvers.NewResolver(
		"inventory", te.Ent, validator.NewValidator(te.DataTypeProvider), te.StockService,
	).Entity()

	got, err := entity.FindPickingOrderItemBySkuAndTenantID(ctx, "unstocked-sku", tenantA)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, int64(0), *got.AvailableStock)
	require.Equal(t, int64(0), *got.ReservedStock)
}
