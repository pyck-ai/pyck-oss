package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

// On Postgres createInventoryItemMovement writes through a PL/pgSQL function
// instead of ent, so the ItemMovement and Stock privacy policies never see
// the write. A READER must still be refused, and the refusal must leave no
// movement, no stock change and no event behind.
func TestItemMovement_Create_RequiresWriter(t *testing.T) {
	t.Parallel()

	readerA := &authn.User{
		ID:       uuid.MustParse("0198a6b2-0000-7000-8000-00000000c001"),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_READER},
	}
	writerA := &authn.User{
		ID:       uuid.MustParse("0198a6b2-0000-7000-8000-00000000c002"),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_WRITER},
	}

	// seed creates a from/to repository pair and an item with 100 units in
	// the from repository, and drains the events the seeding produced.
	seed := func(t *testing.T, te *testEnv) map[string]any {
		t.Helper()
		ctx := te.ctx(userA)
		toRepo := te.newRepository(ctx, userA).Name("To Repository").Create()
		fromRepo := te.newRepository(ctx, userA).Name("From Repository").Create()
		item := te.newItem(ctx, userA).Sku("ROLE-ITEM").Create()
		te.newStock(ctx, userA, item.ID, fromRepo.ID).Quantity(100).Create()
		te.clearEvents(ctx)
		te.ProcEvents.take()
		return map[string]any{
			"ItemID":     item.ID,
			"FromID":     fromRepo.ID,
			"ToID":       toRepo.ID,
			"Handler":    testHandler,
			"BlockedBy":  testBlockedBy.String(),
			"Quantity":   5,
			"DataTypeID": itemDataTypeID,
		}
	}

	t.Run("reader is refused and nothing is written", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		args := seed(t, te)
		adminCtx := te.ctx(userA)

		stocksBefore, err := te.Ent.Stock.Query().Count(adminCtx)
		require.NoError(t, err)
		outboxBefore, err := te.Ent.EntityEventsOutbox.Query().Count(adminCtx)
		require.NoError(t, err)

		execErr(te, te.ctx(readerA), createItemMovement, args, "does not have writer role")

		movements, err := te.Ent.ItemMovement.Query().AllPages(adminCtx, mixin.Limit)
		require.NoError(t, err)
		assert.Empty(t, movements, "a refused reader must not leave a movement behind")

		stocksAfter, err := te.Ent.Stock.Query().Count(adminCtx)
		require.NoError(t, err)
		assert.Equal(t, stocksBefore, stocksAfter, "a refused reader must not write stock snapshots")

		outboxAfter, err := te.Ent.EntityEventsOutbox.Query().Count(adminCtx)
		require.NoError(t, err)
		assert.Equal(t, outboxBefore, outboxAfter, "a refused reader must not write outbox rows")
		assert.Empty(t, te.ProcEvents.take(), "a refused reader must not emit a movement event")
	})

	t.Run("writer creates the movement", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		args := seed(t, te)

		data := execOK[createItemMovementData](te, te.ctx(writerA), createItemMovement, args)
		created := data.CreateInventoryItemMovement.InventoryItemMovement
		assert.Equal(t, int64(5), created.Quantity)
		assert.Equal(t, tenantA, created.TenantID)
	})
}
