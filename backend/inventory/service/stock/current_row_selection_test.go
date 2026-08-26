//nolint:testpackage // in-package test: GetCurrentRepositoriesStock is reached through the package-private service.
package stock

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
)

// Both tests seed a (repo, item) whose created_at order disagrees with its
// version order — what a multi-pod deployment produces under clock skew — and
// pin that the selector follows version. loadAncestorStocks is covered by
// TestExecuteStaleParentRollup_CreatedAtOrderingUnderflows.

// Guards the baseline RebuildStockTable replays through and the delete
// resolvers draw their next version from.
func TestGetCurrentRepositoriesStock_PicksHighestVersion(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)

	repo := e.mkRepo("current-row-repo", uuid.Nil)
	item := e.mkItem("current-row-sku")

	now := time.Now()
	e.mkStockVersionedAt(repo, item, 100, 4, now)               // superseded, later clock
	e.mkStockVersionedAt(repo, item, 7, 5, now.Add(-time.Hour)) // current, earlier clock

	svc := &service{}
	got, err := svc.GetCurrentRepositoriesStock(e.ctx, e.withTx(), []uuid.UUID{repo})
	require.NoError(t, err)

	current := got[repo][item]
	require.Equal(t, int64(5), current.Version,
		"the current row must be the highest version, not the latest created_at")
	require.Equal(t, int64(7), current.Quantity,
		"a superseded row's quantity must not become the rebuild baseline")
}

// Guards the insufficient-stock gate. The empty dialect routes
// CreateItemMovement to the Go path; WithDeferredUnderflow also routes there
// but suppresses the very rejection under test.
func TestCreateItemMovementViaGo_GateReadsHighestVersion(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)

	root := e.mkRepo("gate-root", uuid.Nil)
	from := e.mkRepo("gate-from", root)
	to := e.mkRepo("gate-to", root)
	item := e.mkItem("gate-sku")

	now := time.Now()
	e.mkStockVersionedAt(from, item, 0, 4, now)                  // superseded: reads as nothing available
	e.mkStockVersionedAt(from, item, 10, 5, now.Add(-time.Hour)) // current: 10 available

	svc := &service{}
	movement, err := svc.CreateItemMovement(e.ctx, e.withTx(), CreateItemMovementInput{
		Input: ent.CreateItemMovementInput{
			Quantity: 5,
			Handler:  "test",
			FromID:   from,
			ToID:     to,
			ItemID:   item,
		},
		TenantID: e.tenantID,
	})
	require.NoError(t, err,
		"the gate must read the current row; ordering by created_at rejects this move as insufficient")
	require.NotNil(t, movement)
}
