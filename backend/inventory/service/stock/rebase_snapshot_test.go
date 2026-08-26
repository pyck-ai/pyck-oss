//nolint:testpackage // in-package test: loadStockRebaseSnapshot / newStockVersionTrackerFromFloors / wrapOCCConflict are package-private.
package stock

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// TestLoadStockRebaseSnapshot_LiveAndFloor pins the two halves of the snapshot:
// floor spans soft-deleted rows, live is the highest-version row that is not.
func TestLoadStockRebaseSnapshot_LiveAndFloor(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)

	parent := e.mkRepo("parent", uuid.Nil)
	item := e.mkItem("widget")

	// Live row at v=0, qty=42: this is what the fan-out must use for Quantity.
	e.mkStockVersionedAt(parent, item, 42, 0, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// Soft-deleted row at v=5: highest version, so floor must be 5 and the
	// version tracker must assign 6 next (not 1, which would reuse v=1..4).
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
	require.Equal(t, int64(42), got.Quantity,
		"live quantity must match the seeded non-deleted row")
}

// TestNewStockVersionTrackerFromFloors_MonotonicAssignment verifies that the
// tracker seeded from a floor map assigns floor+1 on the first call and
// increments monotonically for both known and unknown keys.
func TestNewStockVersionTrackerFromFloors_MonotonicAssignment(t *testing.T) {
	t.Parallel()

	k := stockKey{RepositoryID: uuid.New(), ItemID: uuid.New()}
	floor := map[stockKey]int64{k: 5}

	tracker := newStockVersionTrackerFromFloors(floor)

	require.Equal(t, int64(6), tracker.nextFor(k.RepositoryID, k.ItemID),
		"first call must return floor+1")
	require.Equal(t, int64(7), tracker.nextFor(k.RepositoryID, k.ItemID),
		"second call must increment by 1")

	// Unknown key: must start at 0, matching newStockVersionTracker semantics
	// so that (repo, item) pairs with no prior rows begin at version 0.
	unknown := stockKey{RepositoryID: uuid.New(), ItemID: uuid.New()}
	require.Equal(t, int64(0), tracker.nextFor(unknown.RepositoryID, unknown.ItemID),
		"unknown key must start at 0")
	require.Equal(t, int64(1), tracker.nextFor(unknown.RepositoryID, unknown.ItemID),
		"subsequent call for the unknown key must increment")
}

// TestCreateRepositoryMovement_RebaseLiveQuantityUsed drives the full service
// method and pins that the fan-out row carries the live quantity and a version
// above the soft-deleted floor (3 → 4).
//
// Without a concurrent writer both reads see the same data, so this cannot
// observe the delta diverge — no test in the package currently does; see the
// coverage note in pg_concurrent_clobber_test.go.
func TestCreateRepositoryMovement_RebaseLiveQuantityUsed(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)

	// Topology: root → moving (child being moved to) dest.
	// moving.ParentID = root, so the default FROM resolves to root.
	root := e.mkRepo("root", uuid.Nil)
	moving := e.mkRepo("moving", root)
	dest := e.mkRepo("dest", root)

	item := e.mkItem("sku-rebase-create")

	const seedQty int64 = 11

	// Stock at the moving repository so the simulate walk has at least one item.
	e.mkStock(moving, item, seedQty)

	// root/item live row at v=0, qty=seedQty.
	e.mkStockVersionedAt(root, item, seedQty, 0, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// Soft-deleted row at v=3 for root/item: the floor the fan-out must clear.
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

	// The fan-out must have written a root/item row tagged with this movement.
	// Verify quantity (from rebaseLive) and version (from floor+1).
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
		"rebase must preserve the live quantity (delta=0 for Quantity in repo movements)")
	require.Equal(t, int64(4), row.Version,
		"version must be floor+1: soft-deleted v=3 sets floor=3, so next=4")
}

// TestDeleteRepositoryMovement_RebaseLiveQuantityUsed is the Delete-side
// parallel of TestCreateRepositoryMovement_RebaseLiveQuantityUsed. It
// verifies that DeleteRepositoryMovement's fan-out also derives quantity
// from the rebase live snapshot and assigns a version above the soft-deleted
// floor row.
func TestDeleteRepositoryMovement_RebaseLiveQuantityUsed(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)

	root := e.mkRepo("root", uuid.Nil)
	moving := e.mkRepo("moving", root)
	dest := e.mkRepo("dest", root)

	item := e.mkItem("sku-rebase-delete")
	const seedQty int64 = 7

	e.mkStock(moving, item, seedQty)
	e.mkStockVersionedAt(root, item, seedQty, 0, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// Soft-deleted row at v=2 for root/item: floor=2, delete fan-out assigns v≥4
	// (Create fan-out takes v=3, Delete fan-out takes v=4 or higher).
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

	// Both Create and Delete fan-outs write a root/item row tagged with the
	// movement ID. All such rows must carry seedQty as Quantity.
	rows, err := tx.Stock.Query().
		Where(
			entstock.TenantID(e.tenantID),
			entstock.RepositoryID(root),
			entstock.ItemID(item),
			entstock.MovementID(movement.ID),
		).
		AllPages(e.ctx, mixin.Limit)
	require.NoError(t, err)
	require.Len(t, rows, 2, "one row from Create fan-out, one from Delete fan-out")
	for _, r := range rows {
		require.Equal(t, seedQty, r.Quantity,
			"delete rebase must preserve live quantity at version %d", r.Version)
	}
}

// TestRebaseOCCPath_ConflictIsWrapped pins the second half of correct-or-collide:
// a 23505 on the stock version index becomes errOCCConflict so gqltx retries,
// while an unrelated 23505 passes through untouched.
func TestRebaseOCCPath_ConflictIsWrapped(t *testing.T) {
	t.Parallel()

	// Simulate the error Postgres raises on a version-index collision.
	pgErr := &pgconn.PgError{
		Code:           pgerrcode.UniqueViolation,
		ConstraintName: stockOCCUniqueIndex,
	}
	result := wrapOCCConflict(fmt.Errorf("bulk insert: %w", pgErr))
	require.ErrorIs(t, result, errOCCConflict,
		"23505 on the stock version index must become errOCCConflict so gqltx retries")

	// Unrelated unique violation must NOT be mistaken for an OCC conflict.
	otherErr := &pgconn.PgError{
		Code:           pgerrcode.UniqueViolation,
		ConstraintName: "some_other_constraint",
	}
	require.NotErrorIs(t, wrapOCCConflict(fmt.Errorf("x: %w", otherErr)), errOCCConflict,
		"unrelated 23505 must pass through unchanged")
}

// TestRebasedStock_ClampsNegativeReservations covers a concurrent release
// between the two reads: base drops below old, so the subtract walk's delta
// goes negative. The reservation fields must clamp, because Min(0) would raise
// a validation error gqltx does not retry. Quantity stays unclamped.
func TestRebasedStock_ClampsNegativeReservations(t *testing.T) {
	t.Parallel()

	// fresh live: all reservations already released to 0 by a concurrent tx.
	base := ent.Stock{Quantity: 5}
	// stale early read: reservations were 10.
	old := ent.Stock{Quantity: 5, IncomingStock: 10, OutgoingStock: 10, OwnIncomingStock: 10, OwnOutgoingStock: 10}
	// subtract walk reduced them to 3 (removed 7).
	walked := ent.Stock{Quantity: 5, IncomingStock: 3, OutgoingStock: 3, OwnIncomingStock: 3, OwnOutgoingStock: 3}

	got := rebasedStock(base, walked, old)

	// Raw delta: 0 + (3 − 10) = −7. Must be clamped to 0.
	require.Equal(t, int64(0), got.IncomingStock, "IncomingStock raw −7 must clamp to 0")
	require.Equal(t, int64(0), got.OutgoingStock, "OutgoingStock raw −7 must clamp to 0")
	require.Equal(t, int64(0), got.OwnIncomingStock, "OwnIncomingStock raw −7 must clamp to 0")
	require.Equal(t, int64(0), got.OwnOutgoingStock, "OwnOutgoingStock raw −7 must clamp to 0")
	// Quantity delta is 0; live value passes through unchanged.
	require.Equal(t, int64(5), got.Quantity, "Quantity must pass through (delta 0)")
}

// TestRebasedStock_FreshBaseBeatsStaleOld pins that the delta rides on the
// fresh live base, not on the stale values the early read returned.
func TestRebasedStock_FreshBaseBeatsStaleOld(t *testing.T) {
	t.Parallel()

	base := ent.Stock{Quantity: 99, OutgoingStock: 99}   // fresh live row
	old := ent.Stock{Quantity: 10, OutgoingStock: 10}    // stale early read
	walked := ent.Stock{Quantity: 10, OutgoingStock: 17} // create walk added +7 outgoing

	got := rebasedStock(base, walked, old)

	// OutgoingStock: 99 + (17 − 10) = 106. Delta rides on the fresh base.
	require.Equal(t, int64(106), got.OutgoingStock,
		"delta must be applied to the fresh base (99), not the stale old (10)")
	// Quantity delta is 0 (10 − 10); fresh live quantity wins.
	require.Equal(t, int64(99), got.Quantity,
		"Quantity delta 0 → fresh live value 99 passes through")
}
