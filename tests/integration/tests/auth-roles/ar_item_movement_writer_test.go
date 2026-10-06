//go:build integration

package authroles

import (
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"

	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
)

// TestItemMovementCreateRequiresWriter drives createInventoryItemMovement
// through the gateway against Postgres, where the movement is written by a
// PL/pgSQL function rather than ent. A READER holding the inventory gate role
// must be refused and leave no movement behind; a WRITER must still succeed.
func (s *AuthRolesSuite) TestItemMovementCreateRequiresWriter() {
	inventoryRole := serviceroles.Inventory.String()
	writer := gateway.NewInventoryClientForTenant(s.Cfg, s.provisionUser("writer", inventoryRole), s.tenant.ID)
	reader := gateway.NewInventoryClientForTenant(s.Cfg, s.provisionUser("reader", inventoryRole), s.tenant.ID)

	suffix := uuid.NewString()[:8]
	item, err := writer.CreateInventoryItem(s.Ctx, inventoryapi.CreateInventoryItemArgs{
		Input: inventoryapi.CreateInventoryItemInput{Sku: "role-item-" + suffix},
	})
	s.Require().NoError(err, "writer creates the item")
	itemID := item.CreateInventoryItem.InventoryItem.ID

	// A virtual source repository needs no stock, so the movement's only
	// possible refusal is the role check.
	virtual := true
	from, err := writer.CreateInventoryRepository(s.Ctx, inventoryapi.CreateInventoryRepositoryArgs{
		Input: inventoryapi.CreateRepositoryInput{Name: "role-from-" + suffix, Type: entrepository.TypeStatic, VirtualRepo: &virtual},
	})
	s.Require().NoError(err, "writer creates the virtual source repository")
	to, err := writer.CreateInventoryRepository(s.Ctx, inventoryapi.CreateInventoryRepositoryArgs{
		Input: inventoryapi.CreateRepositoryInput{Name: "role-to-" + suffix, Type: entrepository.TypeStatic},
	})
	s.Require().NoError(err, "writer creates the target repository")

	move := inventoryapi.CreateInventoryItemMovementArgs{Input: inventoryapi.CreateItemMovementInput{
		Quantity: 1,
		Handler:  "role-probe",
		FromID:   from.CreateInventoryRepository.InventoryRepository.ID,
		ToID:     to.CreateInventoryRepository.InventoryRepository.ID,
		ItemID:   itemID,
	}}

	movementCount := func() int {
		got, err := writer.GetItemMovements(s.Ctx, inventoryapi.GetItemMovementsArgs{
			Where: &inventoryapi.ItemMovementWhereInput{ItemID: &itemID},
		})
		s.Require().NoError(err, "writer lists the item's movements")
		return got.ItemMovements.TotalCount
	}

	_, err = reader.CreateInventoryItemMovement(s.Ctx, move)
	s.Require().ErrorContains(err, "does not have writer role", "a reader must not create an item movement")
	s.Equal(0, movementCount(), "the refused reader must leave no movement behind")

	_, err = writer.CreateInventoryItemMovement(s.Ctx, move)
	s.Require().NoError(err, "a writer creates the item movement")
	s.Equal(1, movementCount(), "the writer's movement is stored")
}
