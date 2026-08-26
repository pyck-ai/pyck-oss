//nolint:testpackage // in-package test: errOCCConflict / wrapOCCConflict are package-private.
package stock

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestPG_ConcurrentClobber_CreateVsExecute pins the collide half of
// correct-or-collide against real Postgres: two overlapping transactions claim
// the same version for one (tenant, repository, item) triple, the unique index
// admits one, and the loser's 23505 must reach the caller as errOCCConflict so
// gqltx retries. Deterministic, no goroutines — tx1 opens, tx2 commits
// version=1 underneath it, tx1 then tries the same slot.
//
// COVERAGE GAP: this writes stock rows directly, so it never enters
// CreateRepositoryMovement and does not reproduce the two-snapshot clobber of
// #1393 — it passes unchanged against the pre-fix code. The fix's only current
// regression guard is compile-level plus the rebasedStock unit tests. A real
// reproducer needs two concurrent CreateRepositoryMovement transactions over a
// shared ancestor.
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
