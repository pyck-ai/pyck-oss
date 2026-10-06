package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	entinbounditem "github.com/pyck-ai/pyck/backend/receiving/ent/gen/inbounditem"
)

var removeInboundItems = resolver.ParseTemplate(`mutation {
	updateReceivingInbound(id: "{{.ID}}", input: {
		removeInboundItemIDs: [{{range $i, $id := .Remove}}{{if $i}}, {{end}}"{{$id}}"{{end}}]
	}) {
		receivingInbound { id tenantID orderID dataTypeID data }
	}
}`)

var renameAndRemoveInboundItems = resolver.ParseTemplate(`mutation {
	updateReceivingInbound(id: "{{.ID}}", input: {
		orderID: "{{.OrderID}}",
		removeInboundItemIDs: [{{range $i, $id := .Remove}}{{if $i}}, {{end}}"{{$id}}"{{end}}]
	}) {
		receivingInbound { id orderID }
	}
}`)

// removeArgs builds the variables for removeInboundItems.
func removeArgs(inboundID uuid.UUID, remove ...uuid.UUID) map[string]any {
	return map[string]any{"ID": inboundID, "Remove": remove}
}

// TestInbound_RemoveInboundItemIDs pins that removeInboundItemIDs removes only
// items of the inbound being updated. A listed item of another inbound, of
// another tenant, or an unknown id refuses the whole mutation and leaves every
// item untouched.
func TestInbound_RemoveInboundItemIDs(t *testing.T) {
	t.Parallel()

	t.Run("removes the inbound's own item and keeps the others", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		in := te.newInbound(ctx, userA).Create()
		removed := te.newItem(ctx, userA, in.ID).Create()
		kept := te.newItem(ctx, userA, in.ID).Create()
		te.clearEvents(ctx)

		execOK[updateInboundData](te, ctx, removeInboundItems, removeArgs(in.ID, removed.ID))

		te.assertItemDeleted(t, removed.ID, true)
		te.assertItemDeleted(t, kept.ID, false)
		te.assertEvents(ctx, Delete("inbounditem", removed.ID), Update("inbound", in.ID))
	})

	t.Run("refuses an item of another inbound in the same tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		in := te.newInbound(ctx, userA).Create()
		other := te.newInbound(ctx, userA).Create()
		otherItem := te.newItem(ctx, userA, other.ID).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, removeInboundItems, removeArgs(in.ID, otherItem.ID), "not found")

		te.assertItemDeleted(t, otherItem.ID, false)
		te.assertNoEvents(ctx)
	})

	t.Run("refuses an item of another tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctxA, ctxB := te.ctx(userA), te.ctx(userB)

		in := te.newInbound(ctxA, userA).Create()
		inB := te.newInbound(ctxB, userB).Create()
		itemB := te.newItem(ctxB, userB, inB.ID).Create()
		te.clearEvents(ctxA)

		execErr(te, ctxA, removeInboundItems, removeArgs(in.ID, itemB.ID), "not found")

		te.assertItemDeleted(t, itemB.ID, false)
		te.assertNoEvents(ctxA)
	})

	t.Run("refuses an unknown id", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		in := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, removeInboundItems, removeArgs(in.ID, uuid.New()), "not found")

		te.assertNoEvents(ctx)
	})

	t.Run("a repeated own id removes the item once", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		in := te.newInbound(ctx, userA).Create()
		removed := te.newItem(ctx, userA, in.ID).Create()
		kept := te.newItem(ctx, userA, in.ID).Create()
		te.clearEvents(ctx)

		execOK[updateInboundData](te, ctx, removeInboundItems, removeArgs(in.ID, removed.ID, removed.ID))

		te.assertItemDeleted(t, removed.ID, true)
		te.assertItemDeleted(t, kept.ID, false)
		te.assertEvents(ctx, Delete("inbounditem", removed.ID), Update("inbound", in.ID))
	})

	t.Run("a refused list also rolls back the inbound's other changes", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		in := te.newInbound(ctx, userA).OrderID("order-before").Create()
		other := te.newInbound(ctx, userA).Create()
		otherItem := te.newItem(ctx, userA, other.ID).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, renameAndRemoveInboundItems, map[string]any{
			"ID": in.ID, "OrderID": "order-after", "Remove": []uuid.UUID{otherItem.ID},
		}, "not found")

		after, err := te.Ent.Inbound.Get(ctx, in.ID)
		require.NoError(t, err)
		assert.Equal(t, "order-before", after.OrderID, "the inbound's field change must roll back with the refused removal")
		te.assertItemDeleted(t, otherItem.ID, false)
		te.assertNoEvents(ctx)
	})

	t.Run("a foreign id in the list rolls back the inbound's own removal", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		in := te.newInbound(ctx, userA).Create()
		own := te.newItem(ctx, userA, in.ID).Create()
		other := te.newInbound(ctx, userA).Create()
		otherItem := te.newItem(ctx, userA, other.ID).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, removeInboundItems, removeArgs(in.ID, own.ID, otherItem.ID), "not found")

		te.assertItemDeleted(t, own.ID, false)
		te.assertItemDeleted(t, otherItem.ID, false)
		te.assertNoEvents(ctx)
	})
}

// assertItemDeleted reads the item regardless of tenant and soft-delete state
// and checks whether it carries a deleted_at.
func (te *testEnv) assertItemDeleted(t *testing.T, id uuid.UUID, want bool) {
	t.Helper()
	item, err := te.Ent.InboundItem.Query().
		Where(entinbounditem.ID(id)).
		Only(feature.Context(authn.Context(t.Context(), authn.SystemUser()), feature.FEATURE_SHOW_DELETED))
	require.NoError(t, err)
	assert.Equal(t, want, !item.DeletedAt.IsZero(), "item %s deleted_at = %v", id, item.DeletedAt)
}
