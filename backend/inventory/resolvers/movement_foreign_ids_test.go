package resolvers_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"

	entitemmovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/itemmovement"
	entrepositorymovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repositorymovement"
	enttransaction "github.com/pyck-ai/pyck/backend/inventory/ent/gen/transaction"
)

// The tests in this file pin that a movement never stores an id of another
// tenant. The tenant privacy filter scopes the movement row itself, but the
// ids it references (the item of an item movement or of a collection
// position, the target of a repository movement) were stored without an
// ownership check, so tenant A could write rows pointing at tenant B's item
// or re-parent its repository under B's. Each test pairs the refusal with a
// positive control that proves the same call succeeds with A's own ids.

var (
	foreignIDItemMovementTpl = testresolver.ParseTemplate(`mutation {
		createInventoryItemMovement(input: {
			itemID: "{{.ItemID}}", fromID: "{{.FromID}}", toID: "{{.ToID}}",
			handler: "foreign-id", quantity: 1
		}) { inventoryItemMovement { id itemID } }
	}`)

	foreignIDCollectionTpl = testresolver.ParseTemplate(`mutation {
		createInventoryCollectionMovement(input: {
			collection: [{
				itemID: "{{.ItemID}}", fromID: "{{.FromID}}", toID: "{{.ToID}}",
				handler: "foreign-id", quantity: 1
			}]
		}) { id }
	}`)

	foreignIDRepositoryMovementTpl = testresolver.ParseTemplate(`mutation {
		createInventoryRepositoryMovement(input: {
			repositoryID: "{{.RepositoryID}}", toID: "{{.ToID}}", handler: "foreign-id"
		}) { inventoryRepositoryMovement { id toID } }
	}`)
)

type foreignIDItemMovementData struct {
	CreateInventoryItemMovement struct {
		InventoryItemMovement struct {
			ID     uuid.UUID
			ItemID uuid.UUID
		}
	}
}

type foreignIDCollectionData struct {
	CreateInventoryCollectionMovement struct {
		ID uuid.UUID
	}
}

type foreignIDRepositoryMovementData struct {
	CreateInventoryRepositoryMovement struct {
		InventoryRepositoryMovement struct {
			ID   uuid.UUID
			ToID uuid.UUID
		}
	}
}

func TestCreateItemMovementRefusesForeignItem(t *testing.T) {
	t.Parallel()

	te := setup(t)
	t.Cleanup(func() { te.Close(t) })

	ctxA, ctxB := te.ctx(userA), te.ctx(userB)
	bItem := te.newItem(ctxB, userB).Sku("foreign-b-item").Create()
	aItem := te.newItem(ctxA, userA).Sku("foreign-a-item").Create()
	aFrom := te.newRepository(ctxA, userA).Name("foreign-a-virt").Virtual(true).Create()
	aTo := te.newRepository(ctxA, userA).Name("foreign-a-dst").Create()

	own := execOK[foreignIDItemMovementData](te, ctxA, foreignIDItemMovementTpl, map[string]any{
		"ItemID": aItem.ID, "FromID": aFrom.ID, "ToID": aTo.ID,
	})
	assert.Equal(t, aItem.ID, own.CreateInventoryItemMovement.InventoryItemMovement.ItemID, "positive control: A's own item")

	execErr(te, ctxA, foreignIDItemMovementTpl, map[string]any{
		"ItemID": bItem.ID, "FromID": aFrom.ID, "ToID": aTo.ID,
	}, "not found")
	execErr(te, ctxA, foreignIDItemMovementTpl, map[string]any{
		"ItemID": uuid.New(), "FromID": aFrom.ID, "ToID": aTo.ID,
	}, "not found")

	n, err := te.Ent.ItemMovement.Query().Where(entitemmovement.ItemID(bItem.ID)).Count(systemCtx(t))
	require.NoError(t, err)
	assert.Zero(t, n, "no movement row may reference B's item")
}

func TestCreateCollectionMovementRefusesForeignItem(t *testing.T) {
	t.Parallel()

	te := setup(t)
	t.Cleanup(func() { te.Close(t) })

	ctxA, ctxB := te.ctx(userA), te.ctx(userB)
	bItem := te.newItem(ctxB, userB).Sku("foreign-b-item").Create()
	aItem := te.newItem(ctxA, userA).Sku("foreign-a-item").Create()
	aFrom := te.newRepository(ctxA, userA).Name("foreign-a-virt").Virtual(true).Create()
	aTo := te.newRepository(ctxA, userA).Name("foreign-a-dst").Create()

	own := execOK[foreignIDCollectionData](te, ctxA, foreignIDCollectionTpl, map[string]any{
		"ItemID": aItem.ID, "FromID": aFrom.ID, "ToID": aTo.ID,
	})
	assert.NotEqual(t, uuid.Nil, own.CreateInventoryCollectionMovement.ID, "positive control: A's own item")

	execErr(te, ctxA, foreignIDCollectionTpl, map[string]any{
		"ItemID": bItem.ID, "FromID": aFrom.ID, "ToID": aTo.ID,
	}, "not found")

	n, err := te.Ent.ItemMovement.Query().Where(entitemmovement.ItemID(bItem.ID)).Count(systemCtx(t))
	require.NoError(t, err)
	assert.Zero(t, n, "no collection position may reference B's item")
}

func TestCreateRepositoryMovementRefusesForeignTarget(t *testing.T) {
	t.Parallel()

	te := setup(t)
	t.Cleanup(func() { te.Close(t) })

	ctxA, ctxB := te.ctx(userA), te.ctx(userB)
	bRepo := te.newRepository(ctxB, userB).Name("foreign-b-repo").Create()
	// A static parent: the path that loaded the target only for a virtual parent.
	aParent := te.newRepository(ctxA, userA).Name("foreign-a-parent").Create()
	aChild := te.newRepository(ctxA, userA).Name("foreign-a-child").Parent(aParent.ID).Create()
	aTarget := te.newRepository(ctxA, userA).Name("foreign-a-target").Create()

	own := execOK[foreignIDRepositoryMovementData](te, ctxA, foreignIDRepositoryMovementTpl, map[string]any{
		"RepositoryID": aChild.ID, "ToID": aTarget.ID,
	})
	assert.Equal(t, aTarget.ID, own.CreateInventoryRepositoryMovement.InventoryRepositoryMovement.ToID, "positive control: A's own target")

	aChild2 := te.newRepository(ctxA, userA).Name("foreign-a-child-2").Parent(aParent.ID).Create()
	execErr(te, ctxA, foreignIDRepositoryMovementTpl, map[string]any{
		"RepositoryID": aChild2.ID, "ToID": bRepo.ID,
	}, "not found")

	n, err := te.Ent.RepositoryMovement.Query().Where(entrepositorymovement.ToID(bRepo.ID)).Count(systemCtx(t))
	require.NoError(t, err)
	assert.Zero(t, n, "no repository movement may target B's repository")
}

// TestExecuteRepositoryMovementRefusesForeignTarget covers a movement stored
// before the create-time check existed: executing it must not re-parent A's
// repository under B's.
func TestExecuteRepositoryMovementRefusesForeignTarget(t *testing.T) {
	t.Parallel()

	te := setup(t)
	t.Cleanup(func() { te.Close(t) })

	ctxA, ctxB := te.ctx(userA), te.ctx(userB)
	bRepo := te.newRepository(ctxB, userB).Name("foreign-b-repo").Create()
	aParent := te.newRepository(ctxA, userA).Name("foreign-a-parent").Create()
	aChild := te.newRepository(ctxA, userA).Name("foreign-a-child").Parent(aParent.ID).Create()

	// Written directly, as a pre-fix create would have stored it.
	stale := te.newRepositoryMovement(ctxA, userA, aChild.ID, bRepo.ID).FromID(aParent.ID).Create()

	execErr(te, ctxA, executeRepositoryMovement, map[string]any{"ID": stale.ID}, "not found")

	child, err := te.Ent.Repository.Get(systemCtx(t), aChild.ID)
	require.NoError(t, err)
	assert.Equal(t, aParent.ID, child.ParentID, "A's repository must not be re-parented under B's")
}

// TestExecuteItemMovementRefusesForeignItem: a movement stored before items
// were checked at create may name another tenant's item. Executing it must
// not write transactions for that item.
func TestExecuteItemMovementRefusesForeignItem(t *testing.T) {
	t.Parallel()

	te := setup(t)
	t.Cleanup(func() { te.Close(t) })

	ctxA, ctxB := te.ctx(userA), te.ctx(userB)
	bItem := te.newItem(ctxB, userB).DataType(itemDataTypeIDTenantB, itemDataTypeSlug).Create()
	aFrom := te.newRepository(ctxA, userA).Name("foreign-a-from").Virtual(true).Create()
	aTo := te.newRepository(ctxA, userA).Name("foreign-a-to").Create()

	// Written directly, as a pre-fix create would have stored it.
	stale := te.newItemMovement(ctxA, userA, bItem.ID, aFrom.ID, aTo.ID).Quantity(1).Create()

	execErr(te, ctxA, executeItemMovement, map[string]any{"ID": stale.ID}, "not found")

	sys := systemCtx(t)
	n, err := te.Ent.Transaction.Query().Where(enttransaction.ItemID(bItem.ID)).Count(sys)
	require.NoError(t, err)
	assert.Zero(t, n, "executing A's stale movement wrote transactions for B's item")

	mov, err := te.Ent.ItemMovement.Get(sys, stale.ID)
	require.NoError(t, err)
	assert.False(t, mov.Executed, "the refused movement must stay unexecuted")
}

// systemCtx returns a system-user context, which the tenant filter does not
// narrow, so a count sees rows of every tenant.
func systemCtx(t *testing.T) context.Context {
	t.Helper()
	return request.Context(t.Context(), authn.SystemUser(), userA.TenantID)
}
