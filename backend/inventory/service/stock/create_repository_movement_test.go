//nolint:testpackage // in-package test required: CreateRepositoryMovement orchestration is exercised against a private *service receiver to avoid spinning a full resolver.
package stock

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

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
	entrepositorymovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repositorymovement"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// TestCreateRepositoryMovement_UniquenessHookFailsBeforeInsert pins Step
// 5.2 / FINDINGS §3.11 — the repository-movement parallel of the Step 5.1
// item-movement test. The resolver-supplied uniqueness hook
// (validator.ValidateInputDataUniqueness in production) must run BEFORE
// the movement INSERT, not after, so a duplicate-input request fails fast
// without writing the movement row or fanning out the per-item stock rows.
//
// Scenario:
//
//   - Root R with two children A (the moving repository) and B (the TO).
//     The default FROM resolves to repo.ParentID, i.e. R.
//   - A holds a single item so the simulate walk has work to do, exercising
//     the same code path that would write the per-item stock fan-out if
//     the hook fired too late.
//   - CreateRepositoryMovement is invoked with a ValidateUniquenessHook
//     that returns a sentinel error, simulating a duplicate-input
//     collision.
//
// Expected after the failed CreateRepositoryMovement:
//   - The returned error wraps the hook's sentinel verbatim.
//   - tx.RepositoryMovement.Query().Count() returns 0: not a single
//     repository_movement row was inserted before the hook fired.
//   - tx.Stock.Query().Count() shows only the seeded baseline: the per-item
//     stock fan-out also did not run.
//
// Together these two counts pin the BEFORE-Save ordering: if the hook
// fired AFTER Save (the pre-5.2 order), the movement row and every per-
// repo stock snapshot row would already be present in the transaction's
// view even before the rollback.
func TestCreateRepositoryMovement_UniquenessHookFailsBeforeInsert(t *testing.T) {
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
	movingID := mkRepo("A", rootID) // RepositoryID; default FROM is its parent (rootID)
	toID := mkRepo("B", rootID)

	item, err := client.Item.Create().
		SetTenantID(tenantID).
		SetSku("CREATE-REPO-MV-UNIQ-HOOK").
		Save(ctx)
	require.NoError(t, err)
	itemID := item.ID

	const seedQty int64 = 7

	// Seed a stock row at the moving repository so the per-item driver
	// inside CreateRepositoryMovement's simulate loop has at least one
	// iteration; without it the stockMap fan-out is trivially empty and
	// the test would not catch a regression that fanned out before the
	// hook ran.
	_, err = client.Stock.Create().
		SetTenantID(tenantID).
		SetRepositoryID(movingID).
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
	movement, err := svc.CreateRepositoryMovement(ctx, tx, CreateRepositoryMovementInput{
		Input: ent.CreateRepositoryMovementInput{
			Handler:      "test",
			ToID:         toID,
			RepositoryID: movingID,
		},
		TenantID:               tenantID,
		ValidateUniquenessHook: hook,
	})

	require.ErrorIs(t, err, sentinel, "CreateRepositoryMovement must propagate the hook error verbatim")
	require.Nil(t, movement, "no movement value should be returned when the hook errors")
	require.Equal(t, 1, hookCalls, "hook must be invoked exactly once per create")

	// Step 5.2 contract: zero repository_movement rows visible from the
	// same tx after the hook fails. If the hook still ran AFTER Save, this
	// count would be 1 (the row is visible inside its own transaction even
	// before commit).
	mvCount, err := tx.RepositoryMovement.Query().Where(entrepositorymovement.TenantID(tenantID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, mvCount, "no repository movement row may be inserted before a uniqueness conflict aborts the create")

	// And the stockMap fan-out must not have run either: the only stock row
	// is the one seeded above. A regression that fanned out before the hook
	// (or after) would leave fresh per-repo snapshot rows here.
	stockAfter, err := tx.Stock.Query().Where(entstock.TenantID(tenantID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, stockBefore, stockAfter, "per-item stock fan-out must not run when the uniqueness hook fails")
}

// TestCreateRepositoryMovement_VersionTrackerCoversSoftDeletedRow pins the
// repository-movement parallel of the #1199 stock-projection stale-baseline
// race — the surface that PR left unfixed.
//
// CreateRepositoryMovement seeds its stockVersionTracker from ancestorStocks,
// which loadAncestorStocks reads with a DeletedAtIsNil filter. The
// stock_tenant_id_repository_id_item_id_version unique index is NOT partial,
// so it still covers soft-deleted rows. When a soft-deleted stock row already
// holds version 0 for (toID, itemID), the tracker — blind to it — hands out
// version 0 again and the per-item fan-out collides on that index, surfacing
// as the OCC duplicate-key wrap (db.ErrOCCConflict) instead of a clean write.
//
// This is the same defect class as the concurrent-commit race the production
// failure hit (an in-flight transaction committing a stock row between the
// baseline read and the insert); the soft-deleted row is the deterministic,
// SQLite-friendly stand-in for that wider unique-index universe.
//
// Before the fix this fails with the OCC conflict; after seeding the tracker
// from the freshest max(version) across the full index universe (including
// soft-deleted rows), the new TO row lands at version 1.
func TestCreateRepositoryMovement_VersionTrackerCoversSoftDeletedRow(t *testing.T) {
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
	movingID := mkRepo("A", rootID) // RepositoryID; default FROM is its parent (rootID)
	toID := mkRepo("B", rootID)

	item, err := client.Item.Create().
		SetTenantID(tenantID).
		SetSku("CREATE-REPO-MV-VERSION-SOFTDELETE").
		Save(ctx)
	require.NoError(t, err)
	itemID := item.ID

	// Live stock at the moving repository so the simulate fan-out has work.
	_, err = client.Stock.Create().
		SetTenantID(tenantID).
		SetRepositoryID(movingID).
		SetItemID(itemID).
		SetQuantity(7).
		SetOwnQuantity(7).
		SetMovementID(uuid.New()).
		Save(ctx)
	require.NoError(t, err)

	// Soft-deleted stock at the TO repository holding version 0. Invisible to
	// the DeletedAtIsNil baseline, still live on the unique index.
	deleted, err := client.Stock.Create().
		SetTenantID(tenantID).
		SetRepositoryID(toID).
		SetItemID(itemID).
		SetQuantity(0).
		SetOwnQuantity(0).
		SetMovementID(uuid.New()).
		SetVersion(0).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.Stock.UpdateOne(deleted).SetDeletedAt(time.Now()).Save(ctx)
	require.NoError(t, err)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	svc := &service{}
	movement, err := svc.CreateRepositoryMovement(ctx, tx, CreateRepositoryMovementInput{
		Input: ent.CreateRepositoryMovementInput{
			Handler:      "test",
			ToID:         toID,
			RepositoryID: movingID,
		},
		TenantID: tenantID,
	})

	require.NoError(t, err, "repository movement must not collide with a soft-deleted stock version")
	require.NotNil(t, movement)

	// The freshly written TO stock must sit at version 1, above the
	// soft-deleted version 0 it could not previously see.
	latest, err := tx.Stock.Query().
		Where(
			entstock.TenantID(tenantID),
			entstock.RepositoryID(toID),
			entstock.ItemID(itemID),
			entstock.DeletedAtIsNil(),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), latest.Version, "new TO stock row must skip the soft-deleted version 0")
}

// TestCreateRepositoryMovement_DoesNotRewriteUnaffectedAncestorItems pins
// the contract that a repository movement only writes fresh per-item stock
// rows for items physically present on the moving repository. Items that
// happen to sit on a sibling ancestor of FROM/TO but are NOT on the moving
// repo cannot have their rolled-up stock changed by this movement and
// therefore must not be re-stamped.
//
// The bug this guards against is the symptom described in
// issue-create-repository-movement-bulk-insert-exceeds-postgres-param-limit.md:
// CreateRepositoryMovement's stockMap is loaded by loadAncestorStocks with
// itemFilter=nil, so it carries every (repo, item) the closure has. The
// fan-out loop then iterates the full map and writes one fresh stock row
// per pair, producing a bulk INSERT that on production tenants exceeds
// PostgreSQL's 65,535-parameter wire-protocol limit (14 cols * >4,681
// rows). SQLite has no such wire limit, so this test cannot reproduce the
// raw INSERT failure — instead it pins the *root cause*: which items are
// allowed to receive fresh stock rows. The tier-2 architectural fix
// (constrain the fan-out to items at input.RepositoryID) makes the wire
// overflow unreachable as a side-effect; a tier-1 chunking-only fix would
// still write 51 rows here and would still fail this assertion, which is
// the desired pressure away from chunking-as-the-fix.
//
// Topology:
//
//	R (root) -> FROM -> MOVING   (the repo being moved)
//	         \-> ... -> TO       (the destination, sibling of MOVING under FROM)
//
// Seeded stock:
//   - 1 item I_move at MOVING (qty=1) so the simulate loop has work and
//     the per-item driver iterates at least once.
//   - 50 items I_other_n at FROM only (NOT at MOVING). 50 is plenty: the
//     bug fires at any unaffected-item count > 0; we don't need 5,000
//     because we're asserting *identity of the written set*, not the
//     param overflow.
//
// Expected after CreateRepositoryMovement(MOVING -> TO):
//
//	The distinct item_ids across all stock rows tagged with the new
//	movement.ID are exactly {I_move}. The 50 sibling-only items must
//	not appear: their rolled-up stock is unaffected by the move.
func TestCreateRepositoryMovement_DoesNotRewriteUnaffectedAncestorItems(t *testing.T) {
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

	// I_move: the single item physically on MOVING. Only this item's
	// rolled-up stock can change as a result of moving MOVING under TO,
	// so only this item is allowed to receive fresh stock rows.
	iMove, err := client.Item.Create().
		SetTenantID(tenantID).
		SetSku("UNAFFECTED-ITEMS-I-MOVE").
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
	// These items sit on a sibling ancestor and are unaffected by the
	// move; they must not be re-stamped by the fan-out.
	const otherCount = 50
	for i := range otherCount {
		other, ierr := client.Item.Create().
			SetTenantID(tenantID).
			SetSku("UNAFFECTED-ITEMS-I-OTHER-" + strconv.Itoa(i)).
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
	movement, err := svc.CreateRepositoryMovement(ctx, tx, CreateRepositoryMovementInput{
		Input: ent.CreateRepositoryMovementInput{
			Handler:      "test",
			ToID:         toID,
			RepositoryID: movingID,
		},
		TenantID: tenantID,
	})
	require.NoError(t, err)
	require.NotNil(t, movement)

	// The contract: only items physically on the moving repository may
	// receive fresh stock rows tagged with this movement. Group by
	// item_id (avoids .All(ctx) on Stock per limitlint) and compare the
	// distinct set against {iMove.ID}.
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
		"only items physically on the moving repository may get fresh stock rows; "+
			"unaffected ancestor-only items must not be re-stamped",
	)
}
