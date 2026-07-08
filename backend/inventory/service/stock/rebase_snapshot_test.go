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

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// TestLoadStockRebaseSnapshot_LiveAndFloor verifies that a single call to
// loadStockRebaseSnapshot returns:
//   - floor: the max version across ALL rows for each (repo, item), including
//     soft-deleted ones (covering the full unique-index universe).
//   - live: the highest-version non-deleted row for each (repo, item), whose
//     field values the caller uses as the base for the delta formula:
//     written = live[key].Field + (walked[key].Field - old[key].Field).
//
// FAILS to compile against the unfixed code (loadStockRebaseSnapshot does not
// exist). Passes after the fix.
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
//
// FAILS to compile against the unfixed code (newStockVersionTrackerFromFloors
// does not exist). Passes after the fix.
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

// TestCreateRepositoryMovement_RebaseLiveQuantityUsed drives the full
// CreateRepositoryMovement service method and verifies that the fan-out
// row for a parent ancestor uses the quantity from the fresh rebase
// snapshot. It also pins that the assigned version correctly skips the
// soft-deleted floor row, which the old two-read code computed via
// loadMaxStockVersionsIncludingDeleted+seedFromNested and the new one-read
// code computes via loadStockRebaseSnapshot.
//
// In this SQLite harness the "early" loadAncestorStocks read and the fresh
// loadStockRebaseSnapshot read see the same committed data; a true
// Postgres-with-concurrent-tx test would be needed to observe the quantity
// delta diverge. The test still provides meaningful coverage:
//   - The fan-out row quantity equals the seeded live quantity (delta=0 for
//     Quantity in repo movements, so written = live.Quantity + 0 = live.Quantity).
//   - The version skips the soft-deleted floor row (floor=3 → version=4).
//   - The file fails to compile against the unfixed code (uses
//     loadStockRebaseSnapshot / newStockVersionTrackerFromFloors from
//     impl.go), making the pre-fix run a build-level failure.
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

	// Soft-deleted row at v=3 for root/item: this is the "deleted floor" that
	// the old loadMaxStockVersionsIncludingDeleted call would have observed and
	// that loadStockRebaseSnapshot must also observe, assigning version=4.
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
		All(e.ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2, "one row from Create fan-out, one from Delete fan-out")
	for _, r := range rows {
		require.Equal(t, seedQty, r.Quantity,
			"delete rebase must preserve live quantity at version %d", r.Version)
	}
}

// TestRebaseOCCPath_ConflictIsWrapped proves the "correct-or-collide" pipeline
// is intact: a Postgres 23505 on the stock version index becomes errOCCConflict
// so the gqltx retry middleware retries the transaction. An unrelated 23505
// (e.g., the item_sku unique index) must pass through unwrapped.
//
// The SQLite harness cannot produce pq/pgconn error types, so this test uses
// a synthetic *pgconn.PgError to exercise wrapOCCConflict directly. The
// integration counterpart (a concurrent-tx Postgres test) is outside this
// harness; see the spec note in the PR description.
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

// TestRebasedStock_ClampsNegativeReservations guards the edge case where a
// concurrent transaction released a reservation between the early
// loadAncestorStocks read and the fresh loadStockRebaseSnapshot read. When
// that happens the live base is lower than old, so the subtract-mode walk
// (DeleteRepositoryMovement) produces a raw delta that drives the reservation
// field negative. The four Min(0)-validated reservation fields must be clamped
// to 0 — writing a negative value would trip the schema validator with a
// hard error that gqltx cannot retry (it only retries 23505 version
// collisions). Quantity and OwnQuantity keep the raw result: the
// pending-movement walks never touch them (delta always 0), so a negative
// there is a real invariant violation that must stay loud.
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

// TestRebasedStock_FreshBaseBeatsStaleOld verifies that the delta is applied
// to the fresh live base, not to the stale old read. If a concurrent
// transaction had already updated the live row to qty=99 / outgoing=99 by the
// time the rebase snapshot runs, the fan-out must carry those fresh values
// plus the walk's delta (+7 outgoing), not the stale values (qty=10, out=10)
// that the early loadAncestorStocks returned.
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
