//go:build integration

package tenantisolation_test

import (
	"github.com/google/uuid"

	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	inventorymodel "github.com/pyck-ai/pyck/backend/inventory/model"

	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
)

// repositoryEdgeRows holds one tenant-owned row ID per repository edge guarded
// by validateRepositoryEdgeOwnership: a child repository, a stock, an item
// movement, a repository movement, and a transaction — plus a replenishment
// order item for validateReplenishmentOrderEdgeOwnership.
type repositoryEdgeRows struct {
	repoID         string
	stockID        string
	itemMovementID string
	repoMovementID string
	transactionID  string
	orderItemID    string
}

// createRepositoryEdgeRows populates a tenant with one row per repository edge.
// Stock, item-movement, and transaction rows come from executing an item
// movement (the only way to obtain stock/transaction rows, recorded
// synchronously); a repository movement and a replenishment order item are
// created directly.
func (s *TenantIsolationSuite) createRepositoryEdgeRows(cli inventoryapi.Client) repositoryEdgeRows {
	r := s.Require()

	item, err := cli.CreateInventoryItem(s.Ctx, inventoryapi.CreateInventoryItemArgs{
		Input: inventoryapi.CreateInventoryItemInput{Sku: sku()},
	})
	r.NoError(err, "create item")
	itemID := item.CreateInventoryItem.InventoryItem.ID

	virtual := true
	src, err := cli.CreateInventoryRepository(s.Ctx, inventoryapi.CreateInventoryRepositoryArgs{
		Input: inventoryapi.CreateRepositoryInput{
			Name:        "ti-repo-src-" + uuid.NewString(),
			Type:        entrepository.TypeStatic,
			VirtualRepo: &virtual,
		},
	})
	r.NoError(err, "create source repository")
	srcID := src.CreateInventoryRepository.InventoryRepository.ID
	dst, err := cli.CreateInventoryRepository(s.Ctx, inventoryapi.CreateInventoryRepositoryArgs{
		Input: inventoryapi.CreateRepositoryInput{
			Name: "ti-repo-dst-" + uuid.NewString(),
			Type: entrepository.TypeStatic,
		},
	})
	r.NoError(err, "create destination repository")
	dstID := dst.CreateInventoryRepository.InventoryRepository.ID

	mv, err := cli.CreateInventoryItemMovement(s.Ctx, inventoryapi.CreateInventoryItemMovementArgs{
		Input: inventoryapi.CreateItemMovementInput{
			Quantity: 1,
			Handler:  "tenant-isolation-suite",
			FromID:   srcID,
			ToID:     dstID,
			ItemID:   itemID,
		},
	})
	r.NoError(err, "create item movement")
	itemMovementID := mv.CreateInventoryItemMovement.InventoryItemMovement.ID

	_, err = cli.ExecuteInventoryItemMovement(s.Ctx, inventoryapi.ExecuteInventoryItemMovementArgs{Id: itemMovementID})
	r.NoError(err, "execute item movement")

	stocks, err := cli.GetStocks(s.Ctx, inventoryapi.GetStocksArgs{
		Where: &inventoryapi.StockWhereInput{RepositoryID: &dstID},
	})
	r.NoError(err, "get stocks after execution")
	r.NotEmpty(stocks.Stocks.Edges, "executing the movement must record a stock row")

	transactions, err := cli.GetTransactions(s.Ctx, inventoryapi.GetTransactionsArgs{
		Where: &inventoryapi.TransactionWhereInput{RepositoryID: &dstID},
	})
	r.NoError(err, "get transactions after execution")
	r.NotEmpty(transactions.Transactions.Edges, "executing the movement must record a transaction row")

	// A repository movement dereferences the moving repository's parent, so it
	// needs a static parent→child hierarchy; the movement carries the child's
	// contents toward dst.
	notVirtual := false
	parent, err := cli.CreateInventoryRepository(s.Ctx, inventoryapi.CreateInventoryRepositoryArgs{
		Input: inventoryapi.CreateRepositoryInput{Name: "ti-repo-parent-" + uuid.NewString(), Type: entrepository.TypeStatic},
	})
	r.NoError(err, "create parent repository")
	parentID := parent.CreateInventoryRepository.InventoryRepository.ID
	child, err := cli.CreateInventoryRepository(s.Ctx, inventoryapi.CreateInventoryRepositoryArgs{
		Input: inventoryapi.CreateRepositoryInput{
			Name:        "ti-repo-child-" + uuid.NewString(),
			Type:        entrepository.TypeStatic,
			VirtualRepo: &notVirtual,
			ParentID:    &parentID,
		},
	})
	r.NoError(err, "create child repository")
	repoMv, err := cli.CreateInventoryRepositoryMovement(s.Ctx, inventoryapi.CreateInventoryRepositoryMovementArgs{
		Input: inventoryapi.CreateRepositoryMovementInput{
			Handler:      "tenant-isolation-suite",
			ToID:         dstID,
			RepositoryID: child.CreateInventoryRepository.InventoryRepository.ID,
		},
	})
	r.NoError(err, "create repository movement")

	order, err := cli.CreateReplenishmentOrder(s.Ctx, inventoryapi.CreateReplenishmentOrderArgs{
		Input: inventorymodel.CreateReplenishmentOrderWithItemsInput{},
	})
	r.NoError(err, "create replenishment order")
	orderItem, err := cli.CreateReplenishmentOrderItem(s.Ctx, inventoryapi.CreateReplenishmentOrderItemArgs{
		Input: inventoryapi.CreateReplenishmentOrderItemInput{
			Sku:                  sku(),
			Quantity:             1,
			ReplenishmentorderID: order.CreateReplenishmentOrder.ReplenishmentOrder.ID,
		},
	})
	r.NoError(err, "create replenishment order item")

	return repositoryEdgeRows{
		repoID:         dstID,
		stockID:        stocks.Stocks.Edges[0].Node.ID,
		itemMovementID: itemMovementID,
		repoMovementID: repoMv.CreateInventoryRepositoryMovement.InventoryRepositoryMovement.ID,
		transactionID:  transactions.Transactions.Edges[0].Node.ID,
		orderItemID:    orderItem.CreateReplenishmentOrderItem.ReplenishmentOrderItem.ID,
	}
}

// TestRepositoryEdgeAttachIsTenantScoped proves that createInventoryRepository,
// updateInventoryRepository, and updateReplenishmentOrder cannot reference edge
// IDs owned by another tenant — the same cross-tenant edge-attach class the
// item resolvers guard against, on the repository and replenishment-order
// resolvers.
//
// Each distinct target entity behind the repository edges (stock, item
// movement, repository movement, transaction, and a child repository) is
// attacked, plus the replenishment-order-item edge, across the create and
// update paths. As with the item guard, the guard's tenant rejection is
// asserted specifically ("not found in tenant"): several of these edges would
// otherwise fail with an Ent constraint error even without the guard, so a
// bare any-error assertion would not prove tenant scoping.
func (s *TenantIsolationSuite) TestRepositoryEdgeAttachIsTenantScoped() {
	r := s.Require()

	victim := s.provisionTenant()
	attacker := s.provisionTenant()

	victimCli := gateway.NewInventoryClientForTenant(s.Cfg, victim.PAT, victim.Tenant.ID)
	attackerCli := gateway.NewInventoryClientForTenant(s.Cfg, attacker.PAT, attacker.Tenant.ID)

	loot := s.createRepositoryEdgeRows(victimCli)

	// The attacker's own repository, target of the repository update attacks.
	aRepo, err := attackerCli.CreateInventoryRepository(s.Ctx, inventoryapi.CreateInventoryRepositoryArgs{
		Input: inventoryapi.CreateRepositoryInput{Name: "ti-atk-" + uuid.NewString(), Type: entrepository.TypeStatic},
	})
	r.NoError(err, "attacker creates its own repository")
	attackerRepoID := aRepo.CreateInventoryRepository.InventoryRepository.ID

	// The attacker's own replenishment order, target of the order update attack.
	aOrder, err := attackerCli.CreateReplenishmentOrder(s.Ctx, inventoryapi.CreateReplenishmentOrderArgs{
		Input: inventorymodel.CreateReplenishmentOrderWithItemsInput{},
	})
	r.NoError(err, "attacker creates its own replenishment order")
	attackerOrderID := aOrder.CreateReplenishmentOrder.ReplenishmentOrder.ID

	type attack struct {
		name       string
		createRepo *inventoryapi.CreateRepositoryInput
		updateRepo *inventoryapi.UpdateRepositoryInput
		updateOrd  *inventoryapi.UpdateReplenishmentOrderInput
	}
	attacks := []attack{
		// createInventoryRepository — one attack per distinct target entity.
		{name: "create-repo/stock", createRepo: &inventoryapi.CreateRepositoryInput{RepositorystockIDs: []string{loot.stockID}}},
		{name: "create-repo/transaction", createRepo: &inventoryapi.CreateRepositoryInput{RepositorytransactionIDs: []string{loot.transactionID}}},
		{name: "create-repo/itemMovement", createRepo: &inventoryapi.CreateRepositoryInput{ItemmovementtorepositoryIDs: []string{loot.itemMovementID}}},
		{name: "create-repo/repositoryMovement", createRepo: &inventoryapi.CreateRepositoryInput{RepositorymovementrepositoryIDs: []string{loot.repoMovementID}}},
		{name: "create-repo/child", createRepo: &inventoryapi.CreateRepositoryInput{ChildIDs: []string{loot.repoID}}},
		// updateInventoryRepository — add and remove variants across entities.
		{name: "update-repo-add/stock", updateRepo: &inventoryapi.UpdateRepositoryInput{AddRepositoryStockIDs: []string{loot.stockID}}},
		{name: "update-repo-remove/stock", updateRepo: &inventoryapi.UpdateRepositoryInput{RemoveRepositoryStockIDs: []string{loot.stockID}}},
		{name: "update-repo-add/transaction", updateRepo: &inventoryapi.UpdateRepositoryInput{AddRepositoryTransactionIDs: []string{loot.transactionID}}},
		{name: "update-repo-add/itemMovementFrom", updateRepo: &inventoryapi.UpdateRepositoryInput{AddItemMovementFromRepositoryIDs: []string{loot.itemMovementID}}},
		{name: "update-repo-add/repositoryMovement", updateRepo: &inventoryapi.UpdateRepositoryInput{AddRepositoryMovementRepositoryIDs: []string{loot.repoMovementID}}},
		{name: "update-repo-add/child", updateRepo: &inventoryapi.UpdateRepositoryInput{AddChildIDs: []string{loot.repoID}}},
		{name: "update-repo-remove/child", updateRepo: &inventoryapi.UpdateRepositoryInput{RemoveChildIDs: []string{loot.repoID}}},
		// updateReplenishmentOrder — add and remove variants.
		{name: "update-order-add/orderItem", updateOrd: &inventoryapi.UpdateReplenishmentOrderInput{AddReplenishmentOrderItemIDs: []string{loot.orderItemID}}},
		{name: "update-order-remove/orderItem", updateOrd: &inventoryapi.UpdateReplenishmentOrderInput{RemoveReplenishmentOrderItemIDs: []string{loot.orderItemID}}},
	}
	for _, a := range attacks {
		switch {
		case a.createRepo != nil:
			a.createRepo.Name = "ti-atk-" + uuid.NewString()
			a.createRepo.Type = entrepository.TypeStatic
			_, err = attackerCli.CreateInventoryRepository(s.Ctx, inventoryapi.CreateInventoryRepositoryArgs{Input: *a.createRepo})
		case a.updateRepo != nil:
			_, err = attackerCli.UpdateInventoryRepository(s.Ctx, inventoryapi.UpdateInventoryRepositoryArgs{Id: attackerRepoID, Input: *a.updateRepo})
		default:
			_, err = attackerCli.UpdateReplenishmentOrder(s.Ctx, inventoryapi.UpdateReplenishmentOrderArgs{Id: attackerOrderID, Input: *a.updateOrd})
		}
		r.ErrorContains(err, "not found in tenant",
			"%s must reject an edge ID owned by another tenant", a.name)
	}
}
