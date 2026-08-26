//nolint:testpackage // in-package test: errOCCConflict and the fan-out test helpers are package-private.
package stock

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
)

// TestPG_InsertRows_SortsAndWrites covers the contract the DeleteInventoryStock
// fan-out depends on: the batch lands in the database, in the global lock order.
func TestPG_InsertRows_SortsAndWrites(t *testing.T) {
	t.Parallel()
	e := newPGTestEnv(t)

	repos := []uuid.UUID{
		e.mkRepo("insert-rows-a", uuid.Nil),
		e.mkRepo("insert-rows-b", uuid.Nil),
		e.mkRepo("insert-rows-c", uuid.Nil),
	}
	items := []uuid.UUID{e.mkItem("insert-rows-sku-1"), e.mkItem("insert-rows-sku-2")}

	tx := e.withTx()
	creates := make([]*ent.StockCreate, 0, len(repos)*len(items))
	for _, repo := range repos {
		for _, item := range items {
			creates = append(creates, tx.Stock.Create().
				SetTenantID(e.tenantID).
				SetRepositoryID(repo).
				SetItemID(item).
				SetQuantity(1).
				SetOwnQuantity(1).
				SetVersion(0))
		}
	}
	batch := shuffled(creates)

	require.NoError(t, InsertRows(e.ctx, tx, batch))

	requireSortedFanOut(t, batch)

	count, err := tx.Stock.Query().Count(e.ctx)
	require.NoError(t, err)
	require.Equal(t, len(creates), count, "every row in the batch must be written")
}

// TestPG_InsertRows_VersionCollisionIsOCCConflict pins the classification the
// resolver used to lack: a taken version slot reached the operator as a raw
// duplicate-key error, which db.ErrIsRetryable does not recognize.
func TestPG_InsertRows_VersionCollisionIsOCCConflict(t *testing.T) {
	t.Parallel()
	e := newPGTestEnv(t)

	repo := e.mkRepo("insert-rows-occ", uuid.Nil)
	item := e.mkItem("insert-rows-occ-sku")

	const contendedVersion = int64(1)

	_, err := e.client.Stock.Create().
		SetTenantID(e.tenantID).
		SetRepositoryID(repo).
		SetItemID(item).
		SetQuantity(7).
		SetOwnQuantity(7).
		SetVersion(contendedVersion).
		Save(e.ctx)
	require.NoError(t, err)

	tx := e.withTx()
	batch := []*ent.StockCreate{
		tx.Stock.Create().
			SetTenantID(e.tenantID).
			SetRepositoryID(repo).
			SetItemID(item).
			SetQuantity(0).
			SetOwnQuantity(0).
			SetVersion(contendedVersion),
	}

	require.ErrorIs(t, InsertRows(e.ctx, tx, batch), errOCCConflict,
		"a taken version slot must surface as the OCC sentinel so the tx is retried")
}

// TestInsertRows_EmptyBatchIsNoOp: an empty virtual repository fans out
// nothing, and that must not reach the database — the nil tx would panic.
func TestInsertRows_EmptyBatchIsNoOp(t *testing.T) {
	t.Parallel()

	require.NoError(t, InsertRows(t.Context(), nil, nil))
	require.NoError(t, InsertRows(t.Context(), nil, []*ent.StockCreate{}))
}
