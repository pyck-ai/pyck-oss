//nolint:testpackage // in-package test: loadAncestorStocks / applyItemMovementStockDelta are package-private.
package stock

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// mkStockVersionedAt seeds a stock row with an explicit version and quantity,
// then pins its created_at via raw SQL (created_at is Immutable through ent).
// Used to build a (repo, item) whose created_at ordering disagrees with its
// version ordering, which is the state a multi-pod deployment produces under
// clock skew.
func (e *ancestorTestEnv) mkStockVersionedAt(repoID, itemID uuid.UUID, qty, version int64, createdAt time.Time) {
	e.t.Helper()
	row, err := e.client.Stock.Create().
		SetTenantID(e.tenantID).
		SetRepositoryID(repoID).
		SetItemID(itemID).
		SetQuantity(qty).
		SetOwnQuantity(qty).
		SetVersion(version).
		Save(e.ctx)
	require.NoError(e.t, err)

	tx, err := e.client.Tx(e.ctx)
	require.NoError(e.t, err)
	_, err = tx.ExecContext(e.ctx,
		fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s = ?", entstock.Table, entstock.FieldCreatedAt, entstock.FieldID),
		createdAt, row.ID)
	require.NoError(e.t, err)
	require.NoError(e.t, tx.Commit())
}

// TestExecuteStaleParentRollup_CreatedAtOrderingUnderflows guards the fix for
// the production "stock underflow: ... quantity would be -1" that failed
// Hellmann order 003000447808_001.
//
// The latest stock row per (repo, item) must be picked by version, the
// authoritative monotonic order (UNIQUE per tenant/repo/item), not by
// created_at. created_at is the writing pod's wall clock and not a total order
// across workers: a row committed second (higher version) can carry an earlier
// created_at than one written first on a clock-ahead pod. Ordering by created_at
// then returns a superseded row, and ExecuteItemMovement decrements from that
// stale baseline and underflows a valid move.
//
// The test seeds that state: the parent's current rollup is 1 (version 5) while
// a superseded row (version 4, rollup 0) has a later created_at, and the child
// still holds its unit. The move (box to ingoing, qty 1) must succeed (1-1=0);
// it underflows if the latest-row selection orders by created_at.
func TestExecuteStaleParentRollup_CreatedAtOrderingUnderflows(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)

	parent := e.mkRepo("packing-area", uuid.Nil) // rollup parent
	box := e.mkRepo("box", parent)               // child holding the unit
	ingoing := e.mkRepo("ingoing", uuid.Nil)     // disjoint sink (reverse destination)
	item := e.mkItem("side-component")

	// Child still physically holds its unit.
	e.mkStock(box, item, 1)

	// Parent rollup with created_at order inverted vs version order (clock skew):
	// the current state (version 5, rollup 1) has an earlier created_at than the
	// superseded state (version 4, rollup 0).
	older := time.Date(2026, 6, 24, 9, 59, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	e.mkStockVersionedAt(parent, item, 1, 5, older) // current by version
	e.mkStockVersionedAt(parent, item, 0, 4, newer) // superseded, but latest created_at

	tx := e.withTx()
	svc := &service{}

	// Same two steps ExecuteItemMovement runs: load the FROM/TO baseline, then
	// apply the walk with its underflow validation.
	repoMap, priorStocks, err := svc.loadAncestorStocks(
		e.ctx, tx, e.tenantID, []uuid.UUID{box, ingoing}, []uuid.UUID{item}, false)
	require.NoError(t, err)

	gotParent := priorStocks[stockKey{RepositoryID: parent, ItemID: item}]
	t.Logf("parent baseline from loadAncestorStocks: version=%d quantity=%d (true current: version=5 quantity=1)",
		gotParent.Version, gotParent.Quantity)

	stockMap := map[uuid.UUID]ent.Stock{}
	err = svc.applyItemMovementStockDelta(item, box, ingoing, 1, repoMap, priorStocks, stockMap, true)

	require.NoError(t, err,
		"box to ingoing (qty 1) must not underflow: the parent's true rollup is 1 "+
			"(version-latest), so 1-1=0. It only underflows because "+
			"loadAncestorStocks returned the created_at-latest row (rollup 0) instead "+
			"of the version-latest one.")
}
