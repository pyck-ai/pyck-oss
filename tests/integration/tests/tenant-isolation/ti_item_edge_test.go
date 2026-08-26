//go:build integration

package tenantisolation_test

import (
	"github.com/google/uuid"

	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// provisionTenant registers a fresh tenant, schedules its cleanup, and mints a
// writer PAT carrying every per-service gate role.
func (s *TenantIsolationSuite) provisionTenant() *tests.ProvisionedTenant {
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	s.Require().NoError(err, "register tenant")
	s.DeferTenantCleanup(rt.ID)

	p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates("writer"))
	s.Require().NoError(err, "provision user")
	return p
}

func sku() string { return "sku-" + uuid.NewString() }

// itemEdgeRows holds one tenant-owned row ID per item edge guarded by
// validateItemEdgeOwnership: the M2M itemSet edge and the O2M itemStocks /
// itemMovementItems / itemTransactions edges.
type itemEdgeRows struct {
	setID         string
	stockID       string
	movementID    string
	transactionID string
}

// createItemEdgeRows populates a tenant with one row per item edge: an item,
// an item set containing it, and — via a created and executed item movement
// between two fresh repositories — a stock row and a transaction row. Stock
// and transaction rows cannot be created directly through the API; executing
// a movement is the only way to obtain them, and it records both
// synchronously.
func (s *TenantIsolationSuite) createItemEdgeRows(cli inventoryapi.Client) itemEdgeRows {
	r := s.Require()

	item, err := cli.CreateInventoryItem(s.Ctx, inventoryapi.CreateInventoryItemArgs{
		Input: inventoryapi.CreateInventoryItemInput{Sku: sku()},
	})
	r.NoError(err, "create item")
	itemID := item.CreateInventoryItem.InventoryItem.ID

	set, err := cli.CreateInventoryItemSet(s.Ctx, inventoryapi.CreateInventoryItemSetArgs{
		Input: inventoryapi.CreateInventoryItemSetInput{
			Sku:     sku(),
			ItemIDs: []string{itemID},
		},
	})
	r.NoError(err, "create item set")

	// A movement needs a source and a destination; a virtual source can go
	// negative, so no stock has to be seeded first.
	virtual := true
	src, err := cli.CreateInventoryRepository(s.Ctx, inventoryapi.CreateInventoryRepositoryArgs{
		Input: inventoryapi.CreateRepositoryInput{
			Name:        "ti-src-" + uuid.NewString(),
			Type:        entrepository.TypeStatic,
			VirtualRepo: &virtual,
		},
	})
	r.NoError(err, "create source repository")
	dst, err := cli.CreateInventoryRepository(s.Ctx, inventoryapi.CreateInventoryRepositoryArgs{
		Input: inventoryapi.CreateRepositoryInput{
			Name: "ti-dst-" + uuid.NewString(),
			Type: entrepository.TypeStatic,
		},
	})
	r.NoError(err, "create destination repository")
	dstID := dst.CreateInventoryRepository.InventoryRepository.ID

	mv, err := cli.CreateInventoryItemMovement(s.Ctx, inventoryapi.CreateInventoryItemMovementArgs{
		Input: inventoryapi.CreateItemMovementInput{
			Quantity: 1,
			Handler:  "tenant-isolation-suite",
			FromID:   src.CreateInventoryRepository.InventoryRepository.ID,
			ToID:     dstID,
			ItemID:   itemID,
		},
	})
	r.NoError(err, "create item movement")
	movementID := mv.CreateInventoryItemMovement.InventoryItemMovement.ID

	_, err = cli.ExecuteInventoryItemMovement(s.Ctx, inventoryapi.ExecuteInventoryItemMovementArgs{Id: movementID})
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

	return itemEdgeRows{
		setID:         set.CreateInventoryItemSet.InventoryItemSet.ID,
		stockID:       stocks.Stocks.Edges[0].Node.ID,
		movementID:    movementID,
		transactionID: transactions.Transactions.Edges[0].Node.ID,
	}
}

// TestItemEdgeAttachIsTenantScoped proves that createInventoryItem and
// updateInventoryItem cannot reference edge IDs owned by another tenant.
//
// Ent's tenant privacy interceptor scopes the item mutation itself to the
// caller's tenant, but it does not evaluate the privacy policy of the entities
// referenced by the item's edges. Attaching another tenant's itemSet writes a
// cross-tenant M2M link, and the itemStock / itemMovementItem /
// itemTransaction O2M edges repoint the victim row's item_id FK. The sibling
// itemSet resolvers already validate their itemIDs against the caller's
// tenant; the item resolvers must do the same for their edges.
//
// Every ID-carrying edge list is attacked: the four create lists and the four
// add* / four remove* update lists (removing a link on another tenant's row is
// a cross-tenant write too). The clear* update fields carry no IDs — they can
// only detach rows already linked to the caller's own item — so they need no
// guard and no attack. A same-tenant control confirms the guard does not
// reject legitimate attachments.
//
// Without the guard, only the M2M itemSet attacks succeed silently; the O2M
// attacks instead fail with an Ent "already connected" constraint (add) or
// no-op (remove). Every attack must therefore be rejected with the guard's
// own tenant error specifically, not merely with some error — see the
// ErrorContains assertion below.
func (s *TenantIsolationSuite) TestItemEdgeAttachIsTenantScoped() {
	r := s.Require()

	victim := s.provisionTenant()
	attacker := s.provisionTenant()

	victimCli := gateway.NewInventoryClientForTenant(s.Cfg, victim.PAT, victim.Tenant.ID)
	attackerCli := gateway.NewInventoryClientForTenant(s.Cfg, attacker.PAT, attacker.Tenant.ID)

	loot := s.createItemEdgeRows(victimCli)

	// Same-tenant control: the victim attaches its own rows through the exact
	// code paths under test. A guard that rejected everything would pass the
	// attacks below, so prove legitimate use still works. Only the M2M itemSet
	// edge can succeed here: stock / movement / transaction rows are born
	// connected to an item, and Ent's O2M semantics reject re-attaching a
	// connected row ("already connected to a different item_id") regardless of
	// tenant — which is also why the attacks below must assert the tenant
	// validation error specifically, not just any error.
	ownItem, err := victimCli.CreateInventoryItem(s.Ctx, inventoryapi.CreateInventoryItemArgs{
		Input: inventoryapi.CreateInventoryItemInput{
			Sku:        sku(),
			ItemsetIDs: []string{loot.setID},
		},
	})
	r.NoError(err, "createInventoryItem must accept same-tenant edge IDs")
	_, err = victimCli.UpdateInventoryItem(s.Ctx, inventoryapi.UpdateInventoryItemArgs{
		Id:    ownItem.CreateInventoryItem.InventoryItem.ID,
		Input: inventoryapi.UpdateInventoryItemInput{RemoveItemSetIDs: []string{loot.setID}},
	})
	r.NoError(err, "updateInventoryItem must accept same-tenant edge IDs")

	// The attacker's own item, target of the update attacks.
	aItem, err := attackerCli.CreateInventoryItem(s.Ctx, inventoryapi.CreateInventoryItemArgs{
		Input: inventoryapi.CreateInventoryItemInput{Sku: sku()},
	})
	r.NoError(err, "attacker creates its own item")
	attackerItemID := aItem.CreateInventoryItem.InventoryItem.ID

	attacks := []struct {
		name   string
		create *inventoryapi.CreateInventoryItemInput
		update *inventoryapi.UpdateInventoryItemInput
	}{
		{name: "create/itemSet", create: &inventoryapi.CreateInventoryItemInput{ItemsetIDs: []string{loot.setID}}},
		{name: "create/itemStock", create: &inventoryapi.CreateInventoryItemInput{ItemstockIDs: []string{loot.stockID}}},
		{name: "create/itemMovementItem", create: &inventoryapi.CreateInventoryItemInput{ItemmovementitemIDs: []string{loot.movementID}}},
		{name: "create/itemTransaction", create: &inventoryapi.CreateInventoryItemInput{ItemtransactionIDs: []string{loot.transactionID}}},
		{name: "update-add/itemSet", update: &inventoryapi.UpdateInventoryItemInput{AddItemSetIDs: []string{loot.setID}}},
		{name: "update-add/itemStock", update: &inventoryapi.UpdateInventoryItemInput{AddItemStockIDs: []string{loot.stockID}}},
		{name: "update-add/itemMovementItem", update: &inventoryapi.UpdateInventoryItemInput{AddItemMovementItemIDs: []string{loot.movementID}}},
		{name: "update-add/itemTransaction", update: &inventoryapi.UpdateInventoryItemInput{AddItemTransactionIDs: []string{loot.transactionID}}},
		{name: "update-remove/itemSet", update: &inventoryapi.UpdateInventoryItemInput{RemoveItemSetIDs: []string{loot.setID}}},
		{name: "update-remove/itemStock", update: &inventoryapi.UpdateInventoryItemInput{RemoveItemStockIDs: []string{loot.stockID}}},
		{name: "update-remove/itemMovementItem", update: &inventoryapi.UpdateInventoryItemInput{RemoveItemMovementItemIDs: []string{loot.movementID}}},
		{name: "update-remove/itemTransaction", update: &inventoryapi.UpdateInventoryItemInput{RemoveItemTransactionIDs: []string{loot.transactionID}}},
	}
	for _, attack := range attacks {
		if attack.create != nil {
			attack.create.Sku = sku()
			_, err = attackerCli.CreateInventoryItem(s.Ctx, inventoryapi.CreateInventoryItemArgs{Input: *attack.create})
		} else {
			_, err = attackerCli.UpdateInventoryItem(s.Ctx, inventoryapi.UpdateInventoryItemArgs{
				Id:    attackerItemID,
				Input: *attack.update,
			})
		}
		// Assert the guard's tenant rejection specifically: an O2M attack on an
		// already-connected row would fail with an Ent constraint error even
		// without the guard, so any-error would not prove tenant scoping.
		r.ErrorContains(err, "not found in tenant",
			"%s must reject an edge ID owned by another tenant", attack.name)
	}
}
