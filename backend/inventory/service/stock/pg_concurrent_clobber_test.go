//nolint:testpackage // in-package test: errOCCConflict / wrapOCCConflict are package-private.
package stock

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestPG_ConcurrentClobber_CreateVsExecute is the decisive OCC concurrency test.
// Two overlapping transactions both attempt to write the next stock version for the
// same (tenant, repository, item) triple. The unique index
// stock_tenant_id_repository_id_item_id_version admits exactly one; the loser must
// observe errOCCConflict (not a raw 23505) so the gqltx retry middleware retries it.
//
// The test is deterministic — no goroutines:
//  1. tx1 (ent Tx) opens a DB transaction: the "slow" caller that is about to be overtaken.
//  2. tx2 (the shared ent client, auto-commit) commits a new row at version=1.
//  3. tx1 tries to insert at version=1 — the slot tx2 already owns.
//
// Under READ COMMITTED isolation (Postgres default) the unique-index check in step 3
// sees tx2's committed row and raises 23505, which wrapOCCConflict must translate to
// errOCCConflict.
func TestPG_ConcurrentClobber_CreateVsExecute(t *testing.T) {
	t.Parallel()
	e := newPGTestEnv(t)

	repo := e.mkRepo("clobber-repo", uuid.Nil)
	item := e.mkItem("clobber-item")

	// Seed: live stock at version 0.
	_, err := e.client.Stock.Create().
		SetTenantID(e.tenantID).
		SetRepositoryID(repo).
		SetItemID(item).
		SetQuantity(10).
		SetOwnQuantity(10).
		SetVersion(0).
		Save(e.ctx)
	require.NoError(t, err)

	// tx1: open a DB transaction (the "slow" caller). Its view of max_version=0
	// means it intends to write version=1.
	tx1 := e.withTx()

	const nextVer = int64(1)

	// tx2: the "fast" caller — commits at version=1 before tx1 does.
	_, err = e.client.Stock.Create().
		SetTenantID(e.tenantID).
		SetRepositoryID(repo).
		SetItemID(item).
		SetQuantity(11).
		SetOwnQuantity(11).
		SetVersion(nextVer).
		Save(e.ctx)
	require.NoError(t, err)

	// tx1 now tries to insert at version=1 — the slot tx2 already committed.
	// Postgres raises 23505 on the unique index; wrapOCCConflict must surface
	// it as errOCCConflict so the retry middleware retries the transaction.
	_, insertErr := tx1.Stock.Create().
		SetTenantID(e.tenantID).
		SetRepositoryID(repo).
		SetItemID(item).
		SetQuantity(12).
		SetOwnQuantity(12).
		SetVersion(nextVer).
		Save(e.ctx)
	require.ErrorIs(t, wrapOCCConflict(insertErr), errOCCConflict,
		"version slot already committed by a concurrent tx must become errOCCConflict")
}
