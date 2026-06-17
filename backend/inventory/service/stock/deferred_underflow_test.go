//nolint:testpackage // in-package test required: WithDeferredUnderflow gates a private *service receiver and consistencyCheckSourceRows is package-private.
package stock

import (
	"context"
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
	entprivacy "github.com/pyck-ai/pyck/backend/inventory/ent/gen/privacy"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
)

// TestWithDeferredUnderflow_DefersErrorAndConsistencyCatchesIt pins the
// Phase 9.2 contract: when ctx carries WithDeferredUnderflow, the
// direct-create FROM-availability gate is suppressed (the bookkeeping
// runs verbatim, the *error* return is the only thing deferred), and a
// follow-up consistencyCheckSourceRows over the touched (repo, item)
// closure surfaces the underflow that the per-call check would have
// caught.
//
// Scenario:
//
//   - Three leaf repos A, B, C under a single root R.
//   - A holds 1 unit of itemX. B and C hold zero.
//   - Caller plans A->B (qty=5): the FROM-availability gate would
//     normally reject this with errInsufficientStock because A only
//     has 1 unit. Under WithDeferredUnderflow the create returns a
//     successfully persisted movement.
//   - consistencyCheckSourceRows over [A] / [itemX] returns an
//     errStockUnderflow describing the negative effective availability.
func TestWithDeferredUnderflow_DefersErrorAndConsistencyCatchesIt(t *testing.T) {
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
		SetSku("DEFERRED-UNDERFLOW-CATCH").
		Save(ctx)
	require.NoError(t, err)
	itemID := item.ID

	// Seed exactly one unit at A so the FROM gate would normally
	// reject a 5-unit move; under the deferred flag the create still
	// proceeds and consistencyCheckSourceRows is the only thing that
	// can flag the resulting negative final state.
	const seedQty int64 = 1
	_, err = client.Stock.Create().
		SetTenantID(tenantID).
		SetRepositoryID(aID).
		SetItemID(itemID).
		SetQuantity(seedQty).
		SetOwnQuantity(seedQty).
		SetMovementID(uuid.New()).
		Save(ctx)
	require.NoError(t, err)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	deferredCtx := WithDeferredUnderflow(ctx)
	require.True(t, IsDeferredUnderflow(deferredCtx), "WithDeferredUnderflow must flip the flag")
	require.False(t, IsDeferredUnderflow(ctx), "the parent ctx must be unaffected")

	svc := &service{}
	movement, err := svc.CreateItemMovement(deferredCtx, tx, CreateItemMovementInput{
		Input: ent.CreateItemMovementInput{
			Quantity: 5, // greater than seedQty=1, would normally fail
			Handler:  "test",
			FromID:   aID,
			ToID:     bID,
			ItemID:   itemID,
		},
		TenantID: tenantID,
	})
	require.NoError(t, err, "CreateItemMovement must not return errInsufficientStock when the deferred flag is set")
	require.NotNil(t, movement, "the movement row must still be persisted")

	// The consistency pass must surface the underflow that the
	// per-call gate would have raised.
	checkErr := svc.consistencyCheckSourceRows(deferredCtx, tx, tenantID, []uuid.UUID{aID}, []uuid.UUID{itemID})
	require.Error(t, checkErr, "consistencyCheckSourceRows must report the deferred underflow")
	require.ErrorIs(t, checkErr, errStockUnderflow, "consistencyCheckSourceRows must wrap errStockUnderflow")
}

// TestWithDeferredUnderflow_InternallyConsistentChainPasses pins the
// other half of the contract: a chain that *can* settle with non-
// negative final availability must succeed under deferred underflow,
// and consistencyCheckSourceRows must return nil. Per-call rejection
// would still fail the second movement (B has zero stock at the time
// the second call runs against the latest persisted snapshot, since
// the prior incoming reservation at B does count via
// IncomingStock). The point of this test is to confirm that the
// stockMap fan-out runs on the first call (so the second call sees
// updated incoming/quantity values) AND that the final check passes.
//
// Scenario:
//
//   - Three leaf repos A, B, C under a single root R.
//   - A holds 10 units of itemY.
//   - Plan A->B (qty=4) then B->C (qty=4). Without the flag the
//     second call's FROM-availability check sees B's (Quantity=0,
//     IncomingStock=4, OutgoingStock=0) and the gate is satisfied
//     anyway, so this scenario succeeds in both modes; what we pin
//     here is that consistencyCheckSourceRows returns nil for a
//     chain that is internally consistent against A as the only true
//     source.
func TestWithDeferredUnderflow_InternallyConsistentChainPasses(t *testing.T) {
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
	cID := mkRepo("C", rootID)

	item, err := client.Item.Create().
		SetTenantID(tenantID).
		SetSku("DEFERRED-UNDERFLOW-CHAIN-OK").
		Save(ctx)
	require.NoError(t, err)
	itemID := item.ID

	const seedQty int64 = 10
	_, err = client.Stock.Create().
		SetTenantID(tenantID).
		SetRepositoryID(aID).
		SetItemID(itemID).
		SetQuantity(seedQty).
		SetOwnQuantity(seedQty).
		SetMovementID(uuid.New()).
		Save(ctx)
	require.NoError(t, err)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	deferredCtx := WithDeferredUnderflow(ctx)
	svc := &service{}

	// First leg: A -> B, qty=4. A had 10 seeded so this would pass
	// even under per-call rejection; the point is to exercise the
	// bookkeeping under the deferred flag.
	movAB, err := svc.CreateItemMovement(deferredCtx, tx, CreateItemMovementInput{
		Input: ent.CreateItemMovementInput{
			Quantity: 4,
			Handler:  "test",
			FromID:   aID,
			ToID:     bID,
			ItemID:   itemID,
		},
		TenantID: tenantID,
	})
	require.NoError(t, err, "first leg A->B must succeed")
	require.NotNil(t, movAB)

	// Second leg: B -> C, qty=4. B's persisted snapshot now has
	// IncomingStock=4 (from the FROM-walk through the LCA root) /
	// OutgoingStock=0 / Quantity=0 — effective availability is 4, so
	// the FROM gate is satisfied. Run it under the deferred flag to
	// pin the contract end-to-end.
	movBC, err := svc.CreateItemMovement(deferredCtx, tx, CreateItemMovementInput{
		Input: ent.CreateItemMovementInput{
			Quantity: 4,
			Handler:  "test",
			FromID:   bID,
			ToID:     cID,
			ItemID:   itemID,
		},
		TenantID: tenantID,
	})
	require.NoError(t, err, "second leg B->C must succeed under deferred underflow")
	require.NotNil(t, movBC)

	// Final consistency pass: every source row in [A, B] must have
	// non-negative effective availability.
	checkErr := svc.consistencyCheckSourceRows(deferredCtx, tx, tenantID, []uuid.UUID{aID, bID}, []uuid.UUID{itemID})
	require.NoError(t, checkErr, "consistencyCheckSourceRows must return nil for an internally-consistent chain")
}

// TestIsDeferredUnderflow_DefaultFalse pins the opt-in nature of the
// flag: a bare context.Background() (or any ctx that has not gone
// through WithDeferredUnderflow) must produce IsDeferredUnderflow ==
// false. This is the invariant that keeps existing direct-create
// callers' behavior unchanged.
func TestIsDeferredUnderflow_DefaultFalse(t *testing.T) {
	t.Parallel()

	require.False(t, IsDeferredUnderflow(context.Background()), "default ctx must report false")

	type otherKey struct{}
	ctx := context.WithValue(context.Background(), otherKey{}, true)
	require.False(t, IsDeferredUnderflow(ctx), "unrelated ctx values must not flip the flag")
}
