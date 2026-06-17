//nolint:testpackage // in-package test required: insertStockMap is package-private after Step 2.9.4.
package stock

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/request"
	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/enttest"
	entprivacy "github.com/pyck-ai/pyck/backend/inventory/ent/gen/privacy"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// TestInsertStockMap_SkipsNoOpEntries pins Step 3.4: insertStockMap must
// compare each (repo, item) entry against the most recent existing stock
// row for that pair and skip the insert when every quantity field is
// unchanged. Re-inserting an identical snapshot is a write amplification
// loss with no information gain (FINDINGS section 3.4).
//
// As of the stale-baseline race fix (issue-stock-map-resets-own-quantity-
// to-zero-on-pending-pick-creation.md), Quantity and OwnQuantity on the
// to-be-written row are always sourced from the freshly-loaded latest
// baseline (loadLatestStockPerRepo) rather than from the caller's stockMap,
// so a real-change entry must express the change in one of the four
// simulate-driven In/Out fields (IncomingStock, OutgoingStock,
// OwnIncomingStock, OwnOutgoingStock). Quantity / OwnQuantity values on
// stockMap are passthrough only and do not by themselves trigger writes —
// see TestInsertStockMap_BaselineQuantitySourcedFromLatest for the
// converse pinning.
//
// Scenario:
//
//   - Two leaf repos A and B under root R.
//   - Seed an existing stock row at A: Quantity=5, OwnQuantity=5,
//     IncomingStock=2, OutgoingStock=1, OwnIncomingStock=2,
//     OwnOutgoingStock=1.
//   - Build a stockMap that contains:
//   - A: byte-for-byte identical to A's seeded values (no-op);
//   - B: brand new entry with a non-zero OutgoingStock reservation
//     (mimics what simulateRepositoryStockMap produces for a FROM-walk
//     leaf with no prior baseline);
//   - R: brand new entry with all zeros (no-op against the implicit
//     "no prior row" baseline).
//
// Expected after insertStockMap:
//   - The stocks table grew by exactly ONE row (the entry for B).
//   - A's row at the new movement_id is absent: the no-op was skipped.
//   - R has no row at all: a zero entry against a missing baseline is
//     also treated as a no-op (consistent with how the simulate /
//     executor code paths treat unseen baselines as zeros).
func TestInsertStockMap_SkipsNoOpEntries(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, testresolver.DatabaseURI(t))
	t.Cleanup(func() { _ = client.Close() })

	tenantID := uuid.New()
	user := &authn.User{ID: uuid.New(), TenantID: tenantID}
	ctx := request.Context(context.Background(), user, tenantID)
	ctx = entprivacy.DecisionContext(ctx, entprivacy.Allow)

	mkRepo := func(name string, parent uuid.UUID) uuid.UUID {
		t.Helper()
		b := client.Repository.Create().
			SetTenantID(tenantID).
			SetName(name).
			SetType(entrepository.TypeStatic).
			SetVirtualRepo(false)
		if parent != uuid.Nil {
			b.SetParentID(parent)
		}
		repo, err := b.Save(ctx)
		require.NoError(t, err)
		return repo.ID
	}

	rootID := mkRepo("R", uuid.Nil)
	aID := mkRepo("A", rootID)
	bID := mkRepo("B", rootID)

	item, err := client.Item.Create().
		SetTenantID(tenantID).
		SetSku("INSERT-STOCKMAP-NOOP").
		Save(ctx)
	require.NoError(t, err)
	itemID := item.ID

	// Pre-existing stock row for A only. R and B start with no prior row.
	const (
		seedQty = int64(5)
		seedOwn = int64(5)
		seedIn  = int64(2)
		seedOut = int64(1)
	)
	priorMovementID := uuid.New()
	_, err = client.Stock.Create().
		SetTenantID(tenantID).
		SetItemID(itemID).
		SetRepositoryID(aID).
		SetQuantity(seedQty).
		SetOwnQuantity(seedOwn).
		SetIncomingStock(seedIn).
		SetOwnIncomingStock(seedIn).
		SetOutgoingStock(seedOut).
		SetOwnOutgoingStock(seedOut).
		SetMovementID(priorMovementID).
		Save(ctx)
	require.NoError(t, err)

	// Pre-count rows for the (item, tenant) so the post-insert assertion is
	// independent of any rows created by the harness above.
	beforeCount, err := client.Stock.Query().
		Where(entstock.TenantID(tenantID), entstock.ItemID(itemID)).
		Count(ctx)
	require.NoError(t, err)

	// Build a stockMap with one no-op entry (A), one real change (B), and
	// one zero-against-no-prior entry (R) — both no-op cases must be
	// dropped by insertStockMap. The real change at B is expressed as a
	// non-zero OutgoingStock (what simulate would produce for a FROM-walk
	// leaf), not as a Quantity/OwnQuantity value, because those are now
	// race-sourced from the freshly-loaded latest baseline rather than
	// from stockMap.
	const bOutReservation = int64(1)
	stockMap := map[uuid.UUID]map[uuid.UUID]ent.Stock{
		aID: {itemID: ent.Stock{
			Quantity:         seedQty,
			OwnQuantity:      seedOwn,
			IncomingStock:    seedIn,
			OwnIncomingStock: seedIn,
			OutgoingStock:    seedOut,
			OwnOutgoingStock: seedOut,
		}},
		bID: {itemID: ent.Stock{
			OutgoingStock:    bOutReservation,
			OwnOutgoingStock: bOutReservation,
		}},
		rootID: {itemID: ent.Stock{}}, // all zeros; no prior row at R
	}

	movementID := uuid.New()

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	svc := &service{}
	require.NoError(t, svc.insertStockMap(ctx, tx, itemID, tenantID, movementID, stockMap))
	require.NoError(t, tx.Commit())

	afterCount, err := client.Stock.Query().
		Where(entstock.TenantID(tenantID), entstock.ItemID(itemID)).
		Count(ctx)
	require.NoError(t, err)

	require.Equal(t, beforeCount+1, afterCount,
		"expected exactly one new stock row (only the real change at B), got %d → %d", beforeCount, afterCount)

	// Confirm the one new row is the entry for B and is tagged with the new
	// movement ID — the no-op rows must not have been written.
	insertedAtMovement, err := client.Stock.Query().
		Where(entstock.TenantID(tenantID), entstock.ItemID(itemID), entstock.MovementID(movementID)).
		AllPages(ctx, mixin.Limit)
	require.NoError(t, err)
	require.Len(t, insertedAtMovement, 1, "exactly one row should be tagged with the new movement ID")
	require.Equal(t, bID, insertedAtMovement[0].RepositoryID, "the inserted row must be for repository B")
	require.Equal(t, bOutReservation, insertedAtMovement[0].OutgoingStock)
	require.Equal(t, bOutReservation, insertedAtMovement[0].OwnOutgoingStock)
	// Quantity / OwnQuantity at B remain zero: no prior row at B exists,
	// so the freshly-loaded latest baseline is the implicit zero, and the
	// new contract sources Quantity / OwnQuantity from that baseline rather
	// than from stockMap.
	require.Equal(t, int64(0), insertedAtMovement[0].Quantity)
	require.Equal(t, int64(0), insertedAtMovement[0].OwnQuantity)
}

// TestInsertStockMap_InsertsWhenAnyInOutFieldChanges pins the per-field
// granularity of the noop-skip check across the four simulate-driven
// In/Out fields: a delta in any one of IncomingStock, OutgoingStock,
// OwnIncomingStock, or OwnOutgoingStock must trigger an insert, even when
// the other three (and Quantity / OwnQuantity) are unchanged. The
// implementation uses an AND of six equalities; this test guards against
// a regression where one term is dropped from the comparison.
//
// Quantity and OwnQuantity are deliberately NOT exercised here. As of the
// stale-baseline race fix (issue-stock-map-resets-own-quantity-to-zero-on-
// pending-pick-creation.md and stocks_race_test.go), insertStockMap sources
// Quantity / OwnQuantity from the freshly-loaded latest baseline rather
// than from stockMap, so mutating stockMap.Quantity / stockMap.OwnQuantity
// no longer produces an inserted row whose Q / OwnQ differs from latest.
// TestInsertStockMap_BaselineQuantitySourcedFromLatest pins that converse
// behavior; the four In/Out fields below cover the remaining per-field
// granularity that the simulate walk actually drives.
//
// Each subtest runs in its own enttest database (parallel-safe) and seeds
// its own baseline row so the field-delta lookup hits exactly one prior
// snapshot.
func TestInsertStockMap_InsertsWhenAnyInOutFieldChanges(t *testing.T) {
	t.Parallel()

	baseline := ent.Stock{
		Quantity:         3,
		OwnQuantity:      3,
		IncomingStock:    1,
		OwnIncomingStock: 1,
		OutgoingStock:    1,
		OwnOutgoingStock: 1,
	}

	cases := []struct {
		name   string
		mutate func(s *ent.Stock)
	}{
		{"incomingStock", func(s *ent.Stock) { s.IncomingStock++ }},
		{"ownIncomingStock", func(s *ent.Stock) { s.OwnIncomingStock++ }},
		{"outgoingStock", func(s *ent.Stock) { s.OutgoingStock++ }},
		{"ownOutgoingStock", func(s *ent.Stock) { s.OwnOutgoingStock++ }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := enttest.Open(t, dialect.SQLite, testresolver.DatabaseURI(t))
			t.Cleanup(func() { _ = client.Close() })

			tenantID := uuid.New()
			user := &authn.User{ID: uuid.New(), TenantID: tenantID}
			ctx := request.Context(context.Background(), user, tenantID)
			ctx = entprivacy.DecisionContext(ctx, entprivacy.Allow)

			repo, err := client.Repository.Create().
				SetTenantID(tenantID).
				SetName("only").
				SetType(entrepository.TypeStatic).
				SetVirtualRepo(false).
				Save(ctx)
			require.NoError(t, err)
			repoID := repo.ID

			item, err := client.Item.Create().
				SetTenantID(tenantID).
				SetSku("INSERT-STOCKMAP-FIELD-DELTA-" + tc.name).
				Save(ctx)
			require.NoError(t, err)
			itemID := item.ID

			// Seed the baseline row that the lookup will compare against.
			_, err = client.Stock.Create().
				SetTenantID(tenantID).
				SetItemID(itemID).
				SetRepositoryID(repoID).
				SetQuantity(baseline.Quantity).
				SetOwnQuantity(baseline.OwnQuantity).
				SetIncomingStock(baseline.IncomingStock).
				SetOwnIncomingStock(baseline.OwnIncomingStock).
				SetOutgoingStock(baseline.OutgoingStock).
				SetOwnOutgoingStock(baseline.OwnOutgoingStock).
				SetMovementID(uuid.New()).
				Save(ctx)
			require.NoError(t, err)

			beforeCount, err := client.Stock.Query().
				Where(entstock.TenantID(tenantID), entstock.ItemID(itemID), entstock.RepositoryID(repoID)).
				Count(ctx)
			require.NoError(t, err)

			tx, err := client.Tx(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { _ = tx.Rollback() })

			// One field bumped; the other five identical to the baseline.
			mutated := baseline
			tc.mutate(&mutated)

			stockMap := map[uuid.UUID]map[uuid.UUID]ent.Stock{
				repoID: {itemID: mutated},
			}
			svc := &service{}
			require.NoError(t, svc.insertStockMap(ctx, tx, itemID, tenantID, uuid.New(), stockMap))
			require.NoError(t, tx.Commit())

			afterCount, err := client.Stock.Query().
				Where(entstock.TenantID(tenantID), entstock.ItemID(itemID), entstock.RepositoryID(repoID)).
				Count(ctx)
			require.NoError(t, err)

			require.Equal(t, beforeCount+1, afterCount,
				"a single-field change in %s must produce exactly one new stock row", tc.name)
		})
	}
}

// TestInsertStockMap_BaselineQuantitySourcedFromLatest pins the race-safe
// contract introduced by the stale-baseline fix (see
// issue-stock-map-resets-own-quantity-to-zero-on-pending-pick-creation.md
// and stocks_race_test.go): when the caller's stockMap carries Quantity /
// OwnQuantity values that differ from the latest baseline visible to
// loadLatestStockPerRepo, the inserted row uses the LATEST values, not
// the stockMap values. This mirrors the production race where
// loadAncestorStocks (impl.go:553) and loadLatestStockPerRepo
// (impl.go:1620) execute as separate statements under READ COMMITTED and
// a concurrent committer's row becomes visible between them.
//
// In/Out counters are the only fields the caller controls — those are
// validated by TestInsertStockMap_InsertsWhenAnyInOutFieldChanges above.
//
// Scenario:
//
//   - One repo, one item.
//   - Seed a prior stocks row with Quantity=14, OwnQuantity=14
//     (mimicking a concurrent ExecuteItemMovement that wrote a
//     placement after the caller's loadAncestorStocks but before its
//     loadLatestStockPerRepo).
//   - Build a stockMap that simulates the pre-race snapshot: Quantity=0
//     (loadAncestorStocks didn't see the row yet) and a non-zero
//     OutgoingStock=1 (the simulate walk's reservation, which IS sourced
//     from stockMap).
//
// Expected after insertStockMap:
//   - Exactly one new row is inserted (OutgoingStock=1 differs from the
//     seeded baseline's OutgoingStock=0, so the noop-skip does not fire).
//   - The new row's Quantity=14 and OwnQuantity=14 (sourced from the
//     freshly-loaded latest baseline, NOT from the stale stockMap.Quantity=0).
//   - The new row's OutgoingStock=1 (sourced from stockMap, since simulate
//     drives the In/Out counters).
//
// Pre-fix this test would assert Quantity=0 and OwnQuantity=0 (the stale
// poisoned-projection values from the production bug); post-fix it asserts
// 14 / 14 — the values the racing committer wrote — survive the insert.
func TestInsertStockMap_BaselineQuantitySourcedFromLatest(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, testresolver.DatabaseURI(t))
	t.Cleanup(func() { _ = client.Close() })

	tenantID := uuid.New()
	user := &authn.User{ID: uuid.New(), TenantID: tenantID}
	ctx := request.Context(context.Background(), user, tenantID)
	ctx = entprivacy.DecisionContext(ctx, entprivacy.Allow)

	repo, err := client.Repository.Create().
		SetTenantID(tenantID).
		SetName("slot").
		SetType(entrepository.TypeStatic).
		SetVirtualRepo(false).
		Save(ctx)
	require.NoError(t, err)
	repoID := repo.ID

	item, err := client.Item.Create().
		SetTenantID(tenantID).
		SetSku("INSERT-STOCKMAP-BASELINE-FROM-LATEST").
		Save(ctx)
	require.NoError(t, err)
	itemID := item.ID

	// Seed the prior stocks row that loadLatestStockPerRepo (called inside
	// insertStockMap) will pick up. Stands in for the racing committer's
	// just-landed row in the production scenario.
	const (
		latestQty = int64(14)
		latestOwn = int64(14)
	)
	_, err = client.Stock.Create().
		SetTenantID(tenantID).
		SetItemID(itemID).
		SetRepositoryID(repoID).
		SetQuantity(latestQty).
		SetOwnQuantity(latestOwn).
		SetMovementID(uuid.New()).
		Save(ctx)
	require.NoError(t, err)

	// Build the stockMap as the caller would have seen it right BEFORE the
	// race: loadAncestorStocks saw no row (Quantity=0, OwnQuantity=0) and
	// the simulate walk then added a per-leaf OutgoingStock=1 reservation.
	const outReservation = int64(1)
	stockMap := map[uuid.UUID]map[uuid.UUID]ent.Stock{
		repoID: {itemID: ent.Stock{
			Quantity:         0,
			OwnQuantity:      0,
			OutgoingStock:    outReservation,
			OwnOutgoingStock: outReservation,
		}},
	}

	movementID := uuid.New()

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	svc := &service{}
	require.NoError(t, svc.insertStockMap(ctx, tx, itemID, tenantID, movementID, stockMap))
	require.NoError(t, tx.Commit())

	// Exactly one new row tagged with the new movement ID; it must carry
	// Quantity and OwnQuantity sourced from the latest baseline, plus the
	// OutgoingStock reservation sourced from stockMap.
	insertedAtMovement, err := client.Stock.Query().
		Where(entstock.TenantID(tenantID), entstock.ItemID(itemID), entstock.MovementID(movementID)).
		AllPages(ctx, mixin.Limit)
	require.NoError(t, err)
	require.Len(t, insertedAtMovement, 1,
		"exactly one row should be tagged with the new movement ID")

	assert := require.New(t)
	assert.Equal(latestQty, insertedAtMovement[0].Quantity,
		"new row's Quantity must come from the latest baseline (race-safe), "+
			"not from stockMap's stale Quantity=0")
	assert.Equal(latestOwn, insertedAtMovement[0].OwnQuantity,
		"new row's OwnQuantity must come from the latest baseline (race-safe), "+
			"not from stockMap's stale OwnQuantity=0")
	assert.Equal(outReservation, insertedAtMovement[0].OutgoingStock,
		"new row's OutgoingStock must come from stockMap (simulate's delta)")
	assert.Equal(outReservation, insertedAtMovement[0].OwnOutgoingStock,
		"new row's OwnOutgoingStock must come from stockMap (simulate's delta)")
}
