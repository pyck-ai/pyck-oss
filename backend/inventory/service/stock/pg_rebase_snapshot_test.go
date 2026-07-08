//nolint:testpackage // in-package test: loadStockRebaseSnapshot / newStockVersionTrackerFromFloors are package-private.
package stock

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// TestPG_RebaseSnapshot_LiveAndFloor re-exercises TestLoadStockRebaseSnapshot_LiveAndFloor
// against real Postgres to confirm the floor/live query works on the production dialect.
func TestPG_RebaseSnapshot_LiveAndFloor(t *testing.T) {
	t.Parallel()
	e := newPGTestEnv(t)

	parent := e.mkRepo("parent", uuid.Nil)
	item := e.mkItem("widget")

	// Live row at v=0, qty=42.
	_, err := e.client.Stock.Create().
		SetTenantID(e.tenantID).
		SetRepositoryID(parent).
		SetItemID(item).
		SetQuantity(42).
		SetOwnQuantity(42).
		SetVersion(0).
		Save(e.ctx)
	require.NoError(t, err)

	// Soft-deleted row at v=5: floor must be 5 so the version tracker starts at 6.
	row5, err := e.client.Stock.Create().
		SetTenantID(e.tenantID).
		SetRepositoryID(parent).
		SetItemID(item).
		SetQuantity(0).
		SetOwnQuantity(0).
		SetVersion(5).
		Save(e.ctx)
	require.NoError(t, err)
	_, err = e.client.Stock.UpdateOne(row5).SetDeletedAt(time.Now()).Save(e.ctx)
	require.NoError(t, err)

	stockMap := map[uuid.UUID]map[uuid.UUID]ent.Stock{
		parent: {item: {}},
	}

	tx := e.withTx()
	svc := &service{}

	floor, live, err := svc.loadStockRebaseSnapshot(e.ctx, tx, e.tenantID, stockMap)
	require.NoError(t, err)

	k := stockKey{RepositoryID: parent, ItemID: item}
	require.Equal(t, int64(5), floor[k],
		"floor must be max(version) across all rows including soft-deleted")

	got, ok := live[k]
	require.True(t, ok, "live must contain an entry for (parent, item)")
	require.Equal(t, int64(0), got.Version,
		"live must be the highest-version non-deleted row (v=0 is the only live one)")
	require.Equal(t, int64(42), got.Quantity)
}

// TestPG_RebaseSnapshot_CreateFanOut re-exercises TestCreateRepositoryMovement_RebaseLiveQuantityUsed
// against real Postgres. It pins that the Create fan-out uses the live quantity and assigns
// a version above the soft-deleted floor row.
func TestPG_RebaseSnapshot_CreateFanOut(t *testing.T) {
	t.Parallel()
	e := newPGTestEnv(t)

	root := e.mkRepo("root", uuid.Nil)
	moving := e.mkRepo("moving", root)
	dest := e.mkRepo("dest", root)
	item := e.mkItem("sku-rebase-create-pg")

	const seedQty int64 = 11

	e.mkStock(moving, item, seedQty)

	// Live row for root/item at v=0, qty=seedQty.
	_, err := e.client.Stock.Create().
		SetTenantID(e.tenantID).
		SetRepositoryID(root).
		SetItemID(item).
		SetQuantity(seedQty).
		SetOwnQuantity(seedQty).
		SetVersion(0).
		Save(e.ctx)
	require.NoError(t, err)

	// Soft-deleted row at v=3: floor=3, fan-out must assign v=4.
	row3, err := e.client.Stock.Create().
		SetTenantID(e.tenantID).
		SetRepositoryID(root).
		SetItemID(item).
		SetQuantity(0).
		SetOwnQuantity(0).
		SetVersion(3).
		Save(e.ctx)
	require.NoError(t, err)
	_, err = e.client.Stock.UpdateOne(row3).SetDeletedAt(time.Now()).Save(e.ctx)
	require.NoError(t, err)

	tx := e.withTx()
	svc := &service{}

	movement, err := svc.CreateRepositoryMovement(e.ctx, tx, CreateRepositoryMovementInput{
		Input: ent.CreateRepositoryMovementInput{
			Handler:      "test",
			ToID:         dest,
			RepositoryID: moving,
		},
		TenantID: e.tenantID,
	})
	require.NoError(t, err)
	require.NotNil(t, movement)

	row, err := tx.Stock.Query().
		Where(
			entstock.TenantID(e.tenantID),
			entstock.RepositoryID(root),
			entstock.ItemID(item),
			entstock.MovementID(movement.ID),
		).
		Only(e.ctx)
	require.NoError(t, err, "fan-out must have written a stock row for root/item")
	require.Equal(t, seedQty, row.Quantity,
		"rebase must preserve the live quantity")
	require.Equal(t, int64(4), row.Version,
		"version must be floor+1: soft-deleted v=3 sets floor=3, so next=4")
}

// TestPG_RebaseSnapshot_DeleteFanOut re-exercises TestDeleteRepositoryMovement_RebaseLiveQuantityUsed
// against real Postgres. Both Create and Delete fan-out rows must carry the live quantity.
func TestPG_RebaseSnapshot_DeleteFanOut(t *testing.T) {
	t.Parallel()
	e := newPGTestEnv(t)

	root := e.mkRepo("root", uuid.Nil)
	moving := e.mkRepo("moving", root)
	dest := e.mkRepo("dest", root)
	item := e.mkItem("sku-rebase-delete-pg")

	const seedQty int64 = 7

	e.mkStock(moving, item, seedQty)

	_, err := e.client.Stock.Create().
		SetTenantID(e.tenantID).
		SetRepositoryID(root).
		SetItemID(item).
		SetQuantity(seedQty).
		SetOwnQuantity(seedQty).
		SetVersion(0).
		Save(e.ctx)
	require.NoError(t, err)

	// Soft-deleted row at v=2: floor=2.
	row2, err := e.client.Stock.Create().
		SetTenantID(e.tenantID).
		SetRepositoryID(root).
		SetItemID(item).
		SetQuantity(0).
		SetOwnQuantity(0).
		SetVersion(2).
		Save(e.ctx)
	require.NoError(t, err)
	_, err = e.client.Stock.UpdateOne(row2).SetDeletedAt(time.Now()).Save(e.ctx)
	require.NoError(t, err)

	tx := e.withTx()
	svc := &service{}

	movement, err := svc.CreateRepositoryMovement(e.ctx, tx, CreateRepositoryMovementInput{
		Input: ent.CreateRepositoryMovementInput{
			Handler:      "test",
			ToID:         dest,
			RepositoryID: moving,
		},
		TenantID: e.tenantID,
	})
	require.NoError(t, err, "create must succeed")
	require.NotNil(t, movement)

	deleted, err := svc.DeleteRepositoryMovement(e.ctx, tx, DeleteRepositoryMovementInput{
		ID:        movement.ID,
		TenantID:  e.tenantID,
		DeletedBy: uuid.New(),
	})
	require.NoError(t, err, "delete must succeed")
	require.NotNil(t, deleted)

	rows, err := tx.Stock.Query().
		Where(
			entstock.TenantID(e.tenantID),
			entstock.RepositoryID(root),
			entstock.ItemID(item),
			entstock.MovementID(movement.ID),
		).
		All(e.ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2, "one row from Create fan-out, one from Delete fan-out")
	for _, r := range rows {
		require.Equal(t, seedQty, r.Quantity,
			"delete rebase must preserve live quantity at version %d", r.Version)
	}
}
