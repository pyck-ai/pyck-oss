//nolint:testpackage // in-package test required: CreateItemMovement orchestration is exercised against a private *service receiver to avoid spinning a full resolver.
package stock

import (
	"context"
	"errors"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/enttest"
	entitemmovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/itemmovement"
	entprivacy "github.com/pyck-ai/pyck/backend/inventory/ent/gen/privacy"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// TestCreateItemMovement_UniquenessHookFailsBeforeInsert pins Step 5.1 /
// FINDINGS §3.11. The resolver-supplied uniqueness hook
// (validator.ValidateInputDataUniqueness in production) must run BEFORE
// the movement INSERT, not after, so a duplicate-input request fails fast
// without writing the movement row or fanning out the entire stockMap
// snapshot.
//
// Scenario:
//
//   - Two leaf repos A (FROM) and B (TO) under root R, with enough stock
//     seeded at A so the underflow guard would not block the create.
//   - CreateItemMovement is invoked with a ValidateUniquenessHook that
//     returns a sentinel error, simulating a duplicate-input collision.
//
// Expected after the failed CreateItemMovement:
//   - The returned error wraps the hook's sentinel verbatim.
//   - tx.ItemMovement.Query().Count() returns 0: not a single movement
//     row was inserted before the hook fired.
//   - tx.Stock.Query().Count() shows only the seeded baseline: the
//     stockMap fan-out also did not run.
//
// Together these two counts pin the BEFORE-Save ordering: if the hook
// fired AFTER Save (the pre-5.1 order), the movement row and every
// stockMap entry would already be present in the transaction's view even
// before the rollback.
func TestCreateItemMovement_UniquenessHookFailsBeforeInsert(t *testing.T) {
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
	fromID := mkRepo("A", rootID)
	toID := mkRepo("B", rootID)

	item, err := client.Item.Create().
		SetTenantID(tenantID).
		SetSku("CREATE-ITEM-MV-UNIQ-HOOK").
		Save(ctx)
	require.NoError(t, err)
	itemID := item.ID

	const seedQty int64 = 10

	// Seed enough stock at A so the pre-flight underflow guard inside
	// CreateItemMovement does not short-circuit before the hook can run.
	_, err = client.Stock.Create().
		SetTenantID(tenantID).
		SetRepositoryID(fromID).
		SetItemID(itemID).
		SetQuantity(seedQty).
		SetOwnQuantity(seedQty).
		SetMovementID(uuid.New()).
		Save(ctx)
	require.NoError(t, err)

	// Pre-count stock rows so the post-create assertion is independent of
	// whatever rows the harness above produced.
	stockBefore, err := client.Stock.Query().
		Where(entstock.TenantID(tenantID)).
		Count(ctx)
	require.NoError(t, err)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	// The hook stand-in. In production this closure runs
	// validator.ValidateInputDataUniqueness against the JSON-data unique
	// indexes; here we simply return a sentinel so the test pins the
	// ordering contract independent of the validator's internals.
	sentinel := errors.New("duplicate input data")
	hookCalls := 0
	hook := func() error {
		hookCalls++
		return sentinel
	}

	svc := &service{}
	movement, err := svc.CreateItemMovement(ctx, tx, CreateItemMovementInput{
		Input: ent.CreateItemMovementInput{
			Quantity: 1,
			Handler:  "test",
			FromID:   fromID,
			ToID:     toID,
			ItemID:   itemID,
		},
		TenantID:               tenantID,
		ValidateUniquenessHook: hook,
	})

	require.ErrorIs(t, err, sentinel, "CreateItemMovement must propagate the hook error verbatim")
	require.Nil(t, movement, "no movement value should be returned when the hook errors")
	require.Equal(t, 1, hookCalls, "hook must be invoked exactly once per create")

	// Step 5.1 contract: zero movement rows visible from the same tx after
	// the hook fails. If the hook still ran AFTER Save, this count would be
	// 1 (the row is visible inside its own transaction even before commit).
	mvCount, err := tx.ItemMovement.Query().Where(entitemmovement.TenantID(tenantID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, mvCount, "no item movement row may be inserted before a uniqueness conflict aborts the create")

	// And the stockMap fan-out must not have run either: the only stock row
	// is the one seeded above. A regression that fanned out before the hook
	// (or after) would leave fresh per-repo snapshot rows here.
	stockAfter, err := tx.Stock.Query().Where(entstock.TenantID(tenantID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, stockBefore, stockAfter, "stockMap fan-out must not run when the uniqueness hook fails")
}

// TestCreateItemMovement_PreservesQuantityAtCreate pins the create-time
// stock contract: a freshly created (executed=false) movement must update
// the pending counters (incoming_stock / outgoing_stock and their own_*
// counterparts) but MUST NOT touch the actual quantity / own_quantity
// counters. The quantity delta is the responsibility of EXECUTE, applied
// later by ApplyItemMovementStockDelta in ExecuteItemMovement.
//
// Why this contract matters: the quantity delta is applied exactly once,
// at execute time. If create also touched quantity, executing the same
// movement would re-apply the delta and surface as a stock underflow on
// the FROM leaf ("quantity would be -1"). A regression of this contract
// in the Postgres-only create_item_movement_proc (migration
// 20260430070249) caused exactly this failure mode in the
// pyck-projects/hellmann TestHellmannWorkflows integration test on a
// clean tenant — the proc decremented own_quantity at create-time, then
// ExecuteItemMovement decremented again, producing -1 and an infinite
// retry loop on ExecuteCorrectiveMovement. See the SQL fixture
// `testdata/create_item_movement_proc_no_double_apply.sql` (run against
// Postgres) for the proc-side reproduction; this Go test pins the same
// contract on the SQLite Go path so both branches stay aligned.
//
// Scenario:
//
//   - Two leaf repos A (FROM) and B (TO) under root R.
//   - A is seeded with 5 units of an item.
//   - CreateItemMovement is invoked with quantity=2, executed=false.
//
// Expected after CreateItemMovement returns:
//   - The latest stock row for A has quantity=5 and own_quantity=5
//     (unchanged from the seed) and own_outgoing_stock=2 (the new
//     reservation).
//   - The latest stock row for B has quantity=0 and own_quantity=0 (no
//     physical stock yet) and own_incoming_stock=2.
func TestCreateItemMovement_PreservesQuantityAtCreate(t *testing.T) {
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
	fromID := mkRepo("A", rootID)
	toID := mkRepo("B", rootID)

	item, err := client.Item.Create().
		SetTenantID(tenantID).
		SetSku("CREATE-ITEM-MV-PRESERVE-QTY").
		Save(ctx)
	require.NoError(t, err)
	itemID := item.ID

	const seedQty int64 = 5
	const moveQty int64 = 2

	_, err = client.Stock.Create().
		SetTenantID(tenantID).
		SetRepositoryID(fromID).
		SetItemID(itemID).
		SetQuantity(seedQty).
		SetOwnQuantity(seedQty).
		SetMovementID(uuid.New()).
		Save(ctx)
	require.NoError(t, err)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	svc := &service{}
	movement, err := svc.CreateItemMovement(ctx, tx, CreateItemMovementInput{
		Input: ent.CreateItemMovementInput{
			Quantity: moveQty,
			Handler:  "test",
			FromID:   fromID,
			ToID:     toID,
			ItemID:   itemID,
		},
		TenantID: tenantID,
	})
	require.NoError(t, err)
	require.NotNil(t, movement)
	require.False(t, movement.Executed, "CreateItemMovement must persist executed=false")

	latestStock := func(repoID uuid.UUID) *ent.Stock {
		t.Helper()
		s, err := tx.Stock.Query().
			Where(entstock.TenantID(tenantID), entstock.RepositoryID(repoID), entstock.ItemID(itemID)).
			Order(ent.Desc(entstock.FieldCreatedAt)).
			First(ctx)
		require.NoError(t, err)
		return s
	}

	srcStock := latestStock(fromID)
	require.Equal(t, seedQty, srcStock.Quantity,
		"FROM.quantity must remain at the seed value at CREATE time; CREATE moves only the pending counters")
	require.Equal(t, seedQty, srcStock.OwnQuantity,
		"FROM.own_quantity must remain at the seed value at CREATE time; CREATE moves only the pending counters")
	require.Equal(t, moveQty, srcStock.OwnOutgoingStock,
		"FROM.own_outgoing_stock must reflect the new reservation")

	tgtStock := latestStock(toID)
	require.Equal(t, int64(0), tgtStock.Quantity,
		"TO.quantity must remain 0 at CREATE time; nothing physical has moved yet")
	require.Equal(t, int64(0), tgtStock.OwnQuantity,
		"TO.own_quantity must remain 0 at CREATE time; nothing physical has moved yet")
	require.Equal(t, moveQty, tgtStock.OwnIncomingStock,
		"TO.own_incoming_stock must reflect the new reservation")
}
