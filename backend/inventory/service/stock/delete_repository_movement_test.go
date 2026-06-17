//nolint:testpackage // in-package test required: DeleteRepositoryMovement orchestration is exercised against a private *service receiver to avoid spinning a full resolver.
package stock

import (
	"context"
	"strconv"
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
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// TestDeleteRepositoryMovement_DoesNotRewriteUnaffectedAncestorItems is
// the Delete-side parallel of
// TestCreateRepositoryMovement_DoesNotRewriteUnaffectedAncestorItems: it
// pins the contract that the stock fan-out of a repository-movement
// deletion only rewrites rows for items that were physically on the
// moving repository at the time the movement was created. Items sitting
// on a sibling ancestor of FROM/TO but absent from that snapshot cannot
// have their rolled-up stock changed by reversing the movement and
// therefore must not be re-stamped.
//
// Unlike the Create-side parallel this test starts GREEN, not RED:
// DeleteRepositoryMovement already narrows its ancestor-stocks read to
// the items recorded in the movement's create-time snapshot
// (impl.go:1278-1282 — itemIDs from originalQuantities is passed as the
// items filter to loadAncestorStocks), so its stockMap is item-filtered
// by construction and the fan-out can't write rows for items it never
// loaded. The test exists to lock that contract: any future refactor
// that drops the itemIDs filter or otherwise widens the read set will
// trip this assertion the same way the Create-side fix did before
// landing.
//
// Topology and seed are identical to the Create-side test so the two
// assertions are read together:
//
//		R (root) -> FROM -> MOVING   (the repo being moved)
//		         \-> ... -> TO       (the destination, sibling of MOVING under FROM)
//
//	  - 1 item I_move at MOVING (qty=1) — the only item whose rolled-up
//	    stock can change as a result of moving MOVING under TO and back.
//	  - 50 items I_other_n at FROM only — unrelated noise that, if the
//	    contract regressed, would surface as 51 distinct item_ids in the
//	    post-delete stock projection instead of the 1 the contract allows.
//
// Expected after a Create-then-Delete cycle:
//
//	The distinct item_ids across all stock rows tagged with the
//	movement.ID (rows from both the Create fan-out AND the Delete
//	fan-out) equal exactly {I_move}.
func TestDeleteRepositoryMovement_DoesNotRewriteUnaffectedAncestorItems(t *testing.T) {
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

	// Topology: R -> FROM -> MOVING; TO is a sibling of MOVING under FROM.
	// MOVING.ParentID = FROM so the default-FROM resolution inside
	// CreateRepositoryMovement picks FROM as input.FromID.
	rootID := mkRepo("R", uuid.Nil)
	fromID := mkRepo("FROM", rootID)
	movingID := mkRepo("MOVING", fromID)
	toID := mkRepo("TO", fromID)
	_ = rootID // referenced only as the ancestor of FROM via parent linkage.

	// I_move: the single item physically on MOVING. The
	// originalQuantities snapshot the Delete path reconstructs will
	// contain exactly this item; the Delete fan-out is contractually
	// bound to write only for items in that snapshot.
	iMove, err := client.Item.Create().
		SetTenantID(tenantID).
		SetSku("DELETE-UNAFFECTED-I-MOVE").
		Save(ctx)
	require.NoError(t, err)

	_, err = client.Stock.Create().
		SetTenantID(tenantID).
		SetRepositoryID(movingID).
		SetItemID(iMove.ID).
		SetQuantity(1).
		SetOwnQuantity(1).
		SetMovementID(uuid.New()).
		Save(ctx)
	require.NoError(t, err)

	// 50 unrelated items, each with stock at FROM only (not at MOVING).
	// These sit on a sibling ancestor and would only surface in the
	// Delete fan-out if the itemIDs filter were dropped from
	// loadAncestorStocks.
	const otherCount = 50
	for i := range otherCount {
		other, ierr := client.Item.Create().
			SetTenantID(tenantID).
			SetSku("DELETE-UNAFFECTED-I-OTHER-" + strconv.Itoa(i)).
			Save(ctx)
		require.NoError(t, ierr)

		_, serr := client.Stock.Create().
			SetTenantID(tenantID).
			SetRepositoryID(fromID).
			SetItemID(other.ID).
			SetQuantity(7).
			SetOwnQuantity(7).
			SetMovementID(uuid.New()).
			Save(ctx)
		require.NoError(t, serr)
	}

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	svc := &service{}

	// Step 1: create the movement so the Delete path has something to
	// reverse. The Create-side fix is already in place at this point
	// (see TestCreateRepositoryMovement_DoesNotRewriteUnaffectedAncestorItems
	// for the contract Create now obeys), so this call writes stock
	// rows only for iMove across the closure ancestors.
	movement, err := svc.CreateRepositoryMovement(ctx, tx, CreateRepositoryMovementInput{
		Input: ent.CreateRepositoryMovementInput{
			Handler:      "test",
			ToID:         toID,
			RepositoryID: movingID,
		},
		TenantID: tenantID,
	})
	require.NoError(t, err, "create must succeed against the post-fix HEAD")
	require.NotNil(t, movement)

	// Step 2: delete the movement. Internally:
	//   1. reads originalStockRecords WHERE movement_id = movement.ID
	//      AND repository_id = movingID  -> originalQuantities = {iMove: 1}
	//   2. itemIDs := keys(originalQuantities)  -> [iMove.ID]
	//   3. loadAncestorStocks(... itemIDs, false) -> stockMap only
	//      contains entries for iMove across the closure repos
	//   4. fan-out iterates the already-filtered stockMap and writes
	//      fresh rows for (repo, iMove) only.
	deleted, err := svc.DeleteRepositoryMovement(ctx, tx, DeleteRepositoryMovementInput{
		ID:        movement.ID,
		TenantID:  tenantID,
		DeletedBy: user.ID,
	})
	require.NoError(t, err, "delete must succeed against the post-fix HEAD")
	require.NotNil(t, deleted)

	// The contract being pinned: across the full universe of stock
	// rows tagged with this movement.ID (rows from both the Create
	// fan-out and the Delete fan-out), only items physically present
	// on the moving repository at create time may appear. Group by
	// item_id (avoids .All(ctx) on Stock per limitlint) and compare
	// the distinct set against {iMove.ID}.
	//
	// If a future refactor drops the itemIDs filter passed to
	// loadAncestorStocks at impl.go:1282, the Delete fan-out will
	// re-stamp every (closure-ancestor, item) pair and the distinct
	// set will balloon to {iMove + 50 unrelated items}.
	itemIDStrs, err := tx.Stock.Query().
		Where(
			entstock.TenantID(tenantID),
			entstock.MovementID(movement.ID),
		).
		GroupBy(entstock.FieldItemID).
		Strings(ctx)
	require.NoError(t, err)

	require.ElementsMatch(t,
		[]string{iMove.ID.String()},
		itemIDStrs,
		"both the Create and Delete fan-outs must restrict their writes to items "+
			"physically on the moving repository; unaffected ancestor-only items "+
			"must not be re-stamped by either path",
	)
}
