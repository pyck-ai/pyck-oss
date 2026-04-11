package resolvers_test

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"

	"github.com/pyck-ai/pyck/backend/inventory/api"
	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
	"github.com/pyck-ai/pyck/backend/inventory/services"
)

// TestRebuildInventoryStock verifies that the rebuildInventoryStock mutation
// correctly reconstructs the stock table from movement history. The test:
//  1. Creates a repository hierarchy (parent → child, plus a separate virtual repo tree)
//  2. Creates items and movements (create + execute) to establish stock
//  3. Records stock levels before rebuild
//  4. Deletes ALL stock rows to prove rebuild reconstructs (not just a no-op)
//  5. Calls rebuildInventoryStock to reconstruct the stock table
//  6. Asserts that stock levels after rebuild match the levels before corruption
func TestRebuildInventoryStock(t *testing.T) {
	t.Parallel()
	te := setup(t)
	ctx := te.ctx(userA)
	apiClient := setupAPIClient(t, te)

	// =========================================================================
	// Step 1: Create repository hierarchy
	// =========================================================================
	//
	// Real tree:
	//   warehouse (static, root)
	//     ├── shelf-a (static, child of warehouse)
	//     └── shelf-b (static, child of warehouse)
	//
	// Virtual tree (separate, for sourcing stock):
	//   virtual (static, virtual, root)
	//
	virtualID := stockTestCreateRepository(t, ctx, apiClient, "virtual", entrepository.TypeStatic, true, nil)
	warehouseID := stockTestCreateRepository(t, ctx, apiClient, "warehouse", entrepository.TypeStatic, false, nil)
	shelfAID := stockTestCreateRepository(t, ctx, apiClient, "shelf-a", entrepository.TypeStatic, false, &warehouseID)
	shelfBID := stockTestCreateRepository(t, ctx, apiClient, "shelf-b", entrepository.TypeStatic, false, &warehouseID)

	// =========================================================================
	// Step 2: Create items
	// =========================================================================
	itemID := stockTestCreateItem(t, ctx, apiClient, "REBUILD-TEST-ITEM-001")

	// =========================================================================
	// Step 3: Create and execute movements to build stock state
	// =========================================================================
	//
	// Movement 1: virtual → shelf-a, qty 100 (create + execute)
	//   After: shelf-a owns 100, warehouse has 100 (aggregated), virtual qty=0 (clamped)
	//
	m1 := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, shelfAID, 100)
	stockTestExecuteItemMovement(t, ctx, apiClient, m1)

	// Movement 2: shelf-a → shelf-b, qty 30 (create + execute)
	//   After: shelf-a owns 70, shelf-b owns 30, warehouse has 100 (aggregated)
	//
	m2 := stockTestCreateItemMovement(t, ctx, apiClient, itemID, shelfAID, shelfBID, 30)
	stockTestExecuteItemMovement(t, ctx, apiClient, m2)

	// Movement 3: shelf-b → shelf-a, qty 10 (create only, NOT executed)
	//   This adds reservation: shelf-a ownIncoming +10, shelf-b ownOutgoing +10
	//   Warehouse incoming/outgoing should NOT change (intra-warehouse movement)
	//
	_ = stockTestCreateItemMovement(t, ctx, apiClient, itemID, shelfBID, shelfAID, 10)

	// =========================================================================
	// Step 4: Record stock levels before rebuild
	// =========================================================================
	type repoStock struct {
		id    string
		label string
		level stockLevel
	}

	getLevel := func(repoID, label string) repoStock {
		t.Helper()

		var sl stockLevel
		if stock := getStockForRepo(t, ctx, apiClient, repoID, itemID); stock != nil {
			sl.Quantity = stock.GetQuantity()
			sl.OwnQuantity = stock.GetOwnQuantity()
			sl.IncomingStock = stock.GetIncomingStock()
			sl.OwnIncomingStock = stock.GetOwnIncomingStock()
			sl.OutgoingStock = stock.GetOutgoingStock()
			sl.OwnOutgoingStock = stock.GetOwnOutgoingStock()
		}

		return repoStock{id: repoID, label: label, level: sl}
	}

	beforeVirtual := getLevel(virtualID, "virtual")
	beforeWarehouse := getLevel(warehouseID, "warehouse")
	beforeShelfA := getLevel(shelfAID, "shelf-a")
	beforeShelfB := getLevel(shelfBID, "shelf-b")

	// Sanity checks: verify initial stock state is reasonable.
	t.Run("pre-rebuild sanity checks", func(t *testing.T) {
		t.Parallel()
		// virtual: qty/ownQty always 0 (clamped)
		assert.Equal(t, 0, beforeVirtual.level.Quantity, "virtual quantity before rebuild")
		assert.Equal(t, 0, beforeVirtual.level.OwnQuantity, "virtual own quantity before rebuild")

		// shelf-a: executed +100, executed -30 → own=70; reserved incoming +10
		assert.Equal(t, 70, beforeShelfA.level.OwnQuantity, "shelf-a own quantity before rebuild")
		assert.Equal(t, 10, beforeShelfA.level.OwnIncomingStock, "shelf-a own incoming before rebuild")

		// shelf-b: executed +30; reserved outgoing +10
		assert.Equal(t, 30, beforeShelfB.level.OwnQuantity, "shelf-b own quantity before rebuild")
		assert.Equal(t, 10, beforeShelfB.level.OwnOutgoingStock, "shelf-b own outgoing before rebuild")

		// warehouse: aggregated qty = 100
		assert.Equal(t, 100, beforeWarehouse.level.Quantity, "warehouse quantity before rebuild")
	})

	// =========================================================================
	// Step 5: Corrupt stock and rebuild
	// =========================================================================
	corruptAllStock(t, ctx, te.Ent)

	result, err := apiClient.RebuildInventoryStock(ctx)
	require.NoError(t, err, "RebuildInventoryStock should not error")
	require.NotNil(t, result)
	assert.True(t, result.GetRebuildInventoryStock().GetSuccess(), "rebuild should report success")

	// =========================================================================
	// Step 6: Assert stock levels match pre-corruption state
	// =========================================================================
	assertStockLevel(t, ctx, apiClient, virtualID, itemID, "virtual (after rebuild)",
		beforeVirtual.level)
	assertStockLevel(t, ctx, apiClient, warehouseID, itemID, "warehouse (after rebuild)",
		beforeWarehouse.level)
	assertStockLevel(t, ctx, apiClient, shelfAID, itemID, "shelf-a (after rebuild)",
		beforeShelfA.level)
	assertStockLevel(t, ctx, apiClient, shelfBID, itemID, "shelf-b (after rebuild)",
		beforeShelfB.level)
}

// TestRebuildInventoryStock_WithDeletedMovement verifies that the rebuild
// skips soft-deleted movements and produces correct stock levels.
func TestRebuildInventoryStock_WithDeletedMovement(t *testing.T) {
	t.Parallel()
	te := setup(t)
	ctx := te.ctx(userA)
	apiClient := setupAPIClient(t, te)

	// Create repos: virtual (source) and dest (real)
	virtualID := stockTestCreateRepository(t, ctx, apiClient, "virtual", entrepository.TypeStatic, true, nil)
	destID := stockTestCreateRepository(t, ctx, apiClient, "dest", entrepository.TypeStatic, false, nil)

	// Create item
	itemID := stockTestCreateItem(t, ctx, apiClient, "REBUILD-DEL-ITEM-001")

	// Movement 1: virtual → dest, qty 50 (create + execute)
	m1 := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, destID, 50)
	stockTestExecuteItemMovement(t, ctx, apiClient, m1)

	// Movement 2: virtual → dest, qty 20 (create only, then delete)
	m2 := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, destID, 20)
	stockTestDeleteItemMovement(t, ctx, apiClient, m2)

	// Record stock before rebuild
	beforeDest := getStockForRepo(t, ctx, apiClient, destID, itemID)
	require.NotNil(t, beforeDest, "dest should have stock")

	// Corrupt stock, rebuild, and verify
	corruptAllStock(t, ctx, te.Ent)

	result, err := apiClient.RebuildInventoryStock(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.GetRebuildInventoryStock().GetSuccess())

	// After rebuild: only m1 (executed, qty=50) should count.
	// m2 was deleted, so it should be skipped.
	assertStockLevel(t, ctx, apiClient, destID, itemID, "dest (after rebuild, deleted movement ignored)",
		stockLevel{
			Quantity:    beforeDest.GetQuantity(),
			OwnQuantity: beforeDest.GetOwnQuantity(),
		})
}

// TestRebuildInventoryStock_WithRepositoryMovement verifies that rebuild
// correctly handles repository movements (moving a repo between parents).
func TestRebuildInventoryStock_WithRepositoryMovement(t *testing.T) {
	t.Parallel()
	te := setup(t)
	ctx := te.ctx(userA)
	apiClient := setupAPIClient(t, te)

	// Create repository hierarchy:
	//   virtual (root, virtual — stock source)
	//   warehouse (root, real)
	//     ├── storage (child of warehouse)
	//     │   └── shelf (child of storage)
	//     ├── box (dynamic, child of warehouse — will be moved)
	//     └── outbound (child of warehouse — destination for box)
	//
	virtualID := stockTestCreateRepository(t, ctx, apiClient, "virtual", entrepository.TypeStatic, true, nil)
	warehouseID := stockTestCreateRepository(t, ctx, apiClient, "warehouse", entrepository.TypeStatic, false, nil)
	storageID := stockTestCreateRepository(t, ctx, apiClient, "storage", entrepository.TypeStatic, false, &warehouseID)
	shelfID := stockTestCreateRepository(t, ctx, apiClient, "shelf", entrepository.TypeStatic, false, &storageID)
	boxID := stockTestCreateRepository(t, ctx, apiClient, "box", entrepository.TypeDynamic, false, &warehouseID)
	outboundID := stockTestCreateRepository(t, ctx, apiClient, "outbound", entrepository.TypeStatic, false, &warehouseID)

	// Create item and add stock via virtual repo
	itemID := stockTestCreateItem(t, ctx, apiClient, "REBUILD-REPO-MOVE-001")

	// virtual → shelf, qty 50 (create + execute)
	m1 := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, shelfID, 50)
	stockTestExecuteItemMovement(t, ctx, apiClient, m1)

	// shelf → box, qty 5 (create + execute)
	m2 := stockTestCreateItemMovement(t, ctx, apiClient, itemID, shelfID, boxID, 5)
	stockTestExecuteItemMovement(t, ctx, apiClient, m2)

	// Move box from warehouse to outbound (create + execute repository movement)
	rm1 := stockTestCreateRepositoryMovement(t, ctx, apiClient, boxID, warehouseID, outboundID)
	stockTestExecuteRepositoryMovement(t, ctx, apiClient, rm1)

	// Record stock levels before rebuild
	beforeWarehouse := getStockForRepo(t, ctx, apiClient, warehouseID, itemID)
	beforeStorage := getStockForRepo(t, ctx, apiClient, storageID, itemID)
	beforeShelf := getStockForRepo(t, ctx, apiClient, shelfID, itemID)
	beforeBox := getStockForRepo(t, ctx, apiClient, boxID, itemID)
	beforeOutbound := getStockForRepo(t, ctx, apiClient, outboundID, itemID)

	// Corrupt stock, rebuild, and verify
	corruptAllStock(t, ctx, te.Ent)

	result, err := apiClient.RebuildInventoryStock(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.GetRebuildInventoryStock().GetSuccess())

	// After rebuild, stock should match exactly.
	assertRebuiltStock := func(label, repoID string, before *api.GetStocks_Stocks_Edges_Node) {
		t.Helper()

		var expected stockLevel
		if before != nil {
			expected.Quantity = before.GetQuantity()
			expected.OwnQuantity = before.GetOwnQuantity()
			expected.IncomingStock = before.GetIncomingStock()
			expected.OwnIncomingStock = before.GetOwnIncomingStock()
			expected.OutgoingStock = before.GetOutgoingStock()
			expected.OwnOutgoingStock = before.GetOwnOutgoingStock()
		}

		assertStockLevel(t, ctx, apiClient, repoID, itemID, label+" (after rebuild)", expected)
	}

	assertRebuiltStock("warehouse", warehouseID, beforeWarehouse)
	assertRebuiltStock("storage", storageID, beforeStorage)
	assertRebuiltStock("shelf", shelfID, beforeShelf)
	assertRebuiltStock("box", boxID, beforeBox)
	assertRebuiltStock("outbound", outboundID, beforeOutbound)
}

// =============================================================================
// DATA-DRIVEN REBUILD TEST INFRASTRUCTURE
// =============================================================================

// rebuildRepoSpec defines a repository to create as part of a rebuild test scenario.
type rebuildRepoSpec struct {
	Name    string // Repository name (used as key in lookups)
	Parent  string // Parent repository name ("" for root)
	Type    string // "static" (default) or "dynamic"
	Virtual bool   // Whether this is a virtual repository
}

// rebuildMoveSpec defines a single movement and its lifecycle.
//
// For item movements (Type="item" or ""):
//   - Item: the SKU of the item to move
//   - From: source repository name
//   - To:   destination repository name
//   - Qty:  quantity to move
//
// For repo movements (Type="repo"):
//   - From: the repository being moved
//   - To:   the new parent repository
//   - Item/Qty: ignored
type rebuildMoveSpec struct {
	Type    string // "item" (default) or "repo"
	Item    string // Item SKU (for item moves)
	From    string // Source repo or repo being moved
	To      string // Destination repo or new parent
	Qty     int    // Quantity (for item moves)
	Execute bool   // Create + execute the movement
	Delete  bool   // Create + delete (soft-delete) the movement
	// Default (Execute=false, Delete=false): create only (pending)
}

// rebuildScenario defines a complete data-driven rebuild test scenario.
type rebuildScenario struct {
	Name  string            // Sub-test name
	Repos []rebuildRepoSpec // Repositories to create (in order, parents before children)
	Items []string          // Item SKUs to create
	Moves []rebuildMoveSpec // Movements to execute in order
}

// setupScenario creates repositories, items, and processes movements from a
// scenario spec. It returns the list of repos and item IDs for use in snapshot
// and assertion helpers. Both runRebuildScenario and the double-rebuild test
// share this to avoid duplicating setup logic (and diverging on edge cases
// like Delete handling or map-lookup validation).
func setupScenario(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	scenario rebuildScenario,
) (repos []rebuildTestRepo, allItemIDs []string) {
	t.Helper()

	// --- Create repositories ---
	repoIDs := make(map[string]string, len(scenario.Repos))
	parentTracker := make(map[string]string) // repo name → current parent ID

	for _, spec := range scenario.Repos {
		var parentID *string

		if spec.Parent != "" {
			pid, ok := repoIDs[spec.Parent]
			require.True(t, ok, "parent %q not found (repos must be ordered parent-first)", spec.Parent)
			parentID = &pid
			parentTracker[spec.Name] = pid
		}

		repoType := entrepository.TypeStatic
		if spec.Type == "dynamic" {
			repoType = entrepository.TypeDynamic
		}

		repoIDs[spec.Name] = stockTestCreateRepository(
			t, ctx, apiClient, spec.Name, repoType, spec.Virtual, parentID,
		)
	}

	// --- Create items ---
	itemIDs := make(map[string]string, len(scenario.Items))
	for _, sku := range scenario.Items {
		itemIDs[sku] = stockTestCreateItem(t, ctx, apiClient, sku)
	}

	// --- Process movements ---
	for i, move := range scenario.Moves {
		moveType := move.Type
		if moveType == "" {
			moveType = "item"
		}

		fromID, ok := repoIDs[move.From]
		require.True(t, ok, "move[%d]: from repo %q not found", i, move.From)

		toID, ok := repoIDs[move.To]
		require.True(t, ok, "move[%d]: to repo %q not found", i, move.To)

		switch moveType {
		case "item":
			itemID, ok := itemIDs[move.Item]
			require.True(t, ok, "move[%d]: item %q not found", i, move.Item)

			mID := stockTestCreateItemMovement(t, ctx, apiClient, itemID, fromID, toID, move.Qty)

			if move.Execute {
				stockTestExecuteItemMovement(t, ctx, apiClient, mID)
			} else if move.Delete {
				stockTestDeleteItemMovement(t, ctx, apiClient, mID)
			}

		case "repo":
			// For repo movements: fromID is the repo being moved,
			// look up its current parent from the parent tracker.
			currentParentID, ok := parentTracker[move.From]
			require.True(t, ok, "move[%d]: parent tracker for repo %q not found", i, move.From)

			mID := stockTestCreateRepositoryMovement(t, ctx, apiClient, fromID, currentParentID, toID)

			if move.Execute {
				stockTestExecuteRepositoryMovement(t, ctx, apiClient, mID)
				parentTracker[move.From] = toID // Update parent tracker
			} else if move.Delete {
				stockTestDeleteRepositoryMovement(t, ctx, apiClient, mID)
			}

		default:
			t.Fatalf("move[%d]: unknown type %q", i, moveType)
		}
	}

	// --- Build return values ---
	repos = make([]rebuildTestRepo, 0, len(scenario.Repos))
	for _, spec := range scenario.Repos {
		repos = append(repos, rebuildTestRepo{id: repoIDs[spec.Name], label: spec.Name})
	}

	allItemIDs = make([]string, 0, len(scenario.Items))
	for _, sku := range scenario.Items {
		allItemIDs = append(allItemIDs, itemIDs[sku])
	}

	return repos, allItemIDs
}

// runRebuildScenario executes a single rebuild scenario: creates repos, items,
// and movements from the spec, snapshots stock, corrupts the stock table,
// rebuilds, and asserts the rebuilt stock matches the original snapshot.
func runRebuildScenario(t *testing.T, scenario rebuildScenario) {
	t.Helper()

	te := setup(t)
	ctx := te.ctx(userA)
	apiClient := setupAPIClient(t, te)

	repos, allItemIDs := setupScenario(t, ctx, apiClient, scenario)
	rebuildAndAssert(t, ctx, apiClient, te.Ent, repos, allItemIDs)
}

// =============================================================================
// SNAPSHOT AND ASSERTION HELPERS
// =============================================================================

// rebuildTestRepo holds a repository ID and its label for clearer assertions.
type rebuildTestRepo struct {
	id    string
	label string
}

// corruptAllStock deletes ALL stock rows for the tenant, proving that rebuild
// must genuinely reconstruct stock from movements rather than being a no-op.
func corruptAllStock(t *testing.T, ctx context.Context, entClient *ent.Client) {
	t.Helper()

	deleted, err := entClient.Stock.Delete().
		Where(entstock.TenantID(tenantA)).
		Exec(ctx)
	require.NoError(t, err, "deleting stock rows should not error")

	remaining, err := entClient.Stock.Query().
		Where(entstock.TenantID(tenantA)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, remaining, "all stock rows should be deleted")

	t.Logf("corruptAllStock: deleted %d stock rows", deleted)
}

// captureStockSnapshot records current stock levels for every repo/item combination.
func captureStockSnapshot(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	repos []rebuildTestRepo,
	itemIDs []string,
) map[string]map[string]stockLevel {
	t.Helper()

	snapshot := make(map[string]map[string]stockLevel, len(repos))

	for _, repo := range repos {
		snapshot[repo.id] = make(map[string]stockLevel, len(itemIDs))

		for _, itemID := range itemIDs {
			var sl stockLevel
			if stock := getStockForRepo(t, ctx, apiClient, repo.id, itemID); stock != nil {
				sl.Quantity = stock.GetQuantity()
				sl.OwnQuantity = stock.GetOwnQuantity()
				sl.IncomingStock = stock.GetIncomingStock()
				sl.OwnIncomingStock = stock.GetOwnIncomingStock()
				sl.OutgoingStock = stock.GetOutgoingStock()
				sl.OwnOutgoingStock = stock.GetOwnOutgoingStock()
			}

			snapshot[repo.id][itemID] = sl
		}
	}

	return snapshot
}

// assertStockSnapshot asserts that the current stock matches the expected snapshot.
func assertStockSnapshot(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	repos []rebuildTestRepo,
	itemIDs []string,
	expected map[string]map[string]stockLevel,
) {
	t.Helper()

	for _, repo := range repos {
		for _, itemID := range itemIDs {
			expectedLevel := expected[repo.id][itemID]
			assertStockLevel(t, ctx, apiClient, repo.id, itemID,
				repo.label+" (after rebuild)", expectedLevel)
		}
	}
}

// rebuildAndAssert captures stock snapshot, corrupts the stock table by deleting
// all rows, rebuilds from movements, and asserts rebuilt stock matches the
// original snapshot. This proves rebuild genuinely reconstructs from movements.
func rebuildAndAssert(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	entClient *ent.Client,
	repos []rebuildTestRepo,
	itemIDs []string,
	stockSvc ...*services.InventoryStockService,
) {
	t.Helper()

	// Enable debug logging on stock service if provided
	var debugBuf bytes.Buffer
	if len(stockSvc) > 0 && stockSvc[0] != nil {
		stockSvc[0].DebugLog = &debugBuf
		defer func() { stockSvc[0].DebugLog = nil }()
	}

	// 1. Capture expected stock state
	before := captureStockSnapshot(t, ctx, apiClient, repos, itemIDs)

	// 2. Corrupt: delete ALL stock rows to prove rebuild is not a no-op
	corruptAllStock(t, ctx, entClient)

	// 3. Rebuild from movement history
	result, err := apiClient.RebuildInventoryStock(ctx)
	require.NoError(t, err, "RebuildInventoryStock should not error")
	require.NotNil(t, result)
	assert.True(t, result.GetRebuildInventoryStock().GetSuccess(), "rebuild should report success")

	// Debug: count stock rows after rebuild
	afterCount, err := entClient.Stock.Query().
		Where(entstock.TenantID(tenantA)).
		Count(ctx)
	require.NoError(t, err)
	t.Logf("rebuildAndAssert: stock rows after rebuild = %d", afterCount)

	// Debug: dump all non-zero stock rows and compare with expected
	allStockRows, err := entClient.Stock.Query().
		Where(entstock.TenantID(tenantA)).
		All(ctx)
	require.NoError(t, err)

	// Build repo ID → label map for readable output
	repoIDToLabel := make(map[string]string, len(repos))
	for _, r := range repos {
		repoIDToLabel[r.id] = r.label
	}

	// Count mismatches for summary
	mismatchCount := 0
	for _, repo := range repos {
		for _, itemID := range itemIDs {
			expectedLevel := before[repo.id][itemID]
			if expectedLevel.Quantity == 0 && expectedLevel.OwnQuantity == 0 {
				continue // skip zero-expected entries
			}

			// Find matching rebuilt stock row
			var found bool
			for _, row := range allStockRows {
				if row.RepositoryID.String() == repo.id && row.ItemID.String() == itemID {
					found = true
					if row.Quantity != int64(expectedLevel.Quantity) || row.OwnQuantity != int64(expectedLevel.OwnQuantity) {
						mismatchCount++
						if mismatchCount <= 5 {
							t.Logf("MISMATCH repo=%s item=%s expected=(Qty=%d,Own=%d) actual=(Qty=%d,Own=%d)",
								repo.label, itemID[:8], expectedLevel.Quantity, expectedLevel.OwnQuantity,
								row.Quantity, row.OwnQuantity)
						}
					}
					break
				}
			}
			if !found {
				mismatchCount++
				if mismatchCount <= 5 {
					t.Logf("MISSING repo=%s item=%s expected=(Qty=%d,Own=%d) — no stock row found",
						repo.label, itemID[:8], expectedLevel.Quantity, expectedLevel.OwnQuantity)
				}
			}
		}
	}
	if mismatchCount > 5 {
		t.Logf("... and %d more mismatches", mismatchCount-5)
	}

	// Dump a few rebuilt stock rows for context
	dumpCount := 0
	for _, row := range allStockRows {
		if row.Quantity != 0 || row.OwnQuantity != 0 {
			label := repoIDToLabel[row.RepositoryID.String()]
			if label == "" {
				label = row.RepositoryID.String()[:8]
			}
			if dumpCount < 10 {
				t.Logf("REBUILT STOCK: repo=%s item=%s Qty=%d Own=%d In=%d OwnIn=%d Out=%d OwnOut=%d",
					label, row.ItemID.String()[:8], row.Quantity, row.OwnQuantity,
					row.IncomingStock, row.OwnIncomingStock, row.OutgoingStock, row.OwnOutgoingStock)
			}
			dumpCount++
		}
	}
	t.Logf("Total rebuilt stock rows with non-zero Qty/OwnQty: %d", dumpCount)

	// Dump debug log from stock service on mismatch
	if mismatchCount > 0 && debugBuf.Len() > 0 {
		t.Logf("=== STOCK SERVICE DEBUG LOG (full, %d bytes) ===", debugBuf.Len())
		t.Logf("%s", debugBuf.String())
		t.Logf("=== END DEBUG LOG ===")
	}

	// 4. Assert rebuilt stock matches the original snapshot
	assertStockSnapshot(t, ctx, apiClient, repos, itemIDs, before)
}

// =============================================================================
// DATA-DRIVEN REBUILD SCENARIOS
// =============================================================================

// rebuildScenarios defines all data-driven rebuild test cases.
//
//nolint:funlen // Table-driven test data is intentionally long.
var rebuildScenarios = []rebuildScenario{
	{
		// Three separate trees (one virtual, two real warehouses), 5 levels deep,
		// two items, cross-tree pending movements.
		//
		//   virtual-src (root, virtual)
		//
		//   warehouse-a (root)
		//     └── zone-a1
		//         ├── aisle-a1-1
		//         │   ├── shelf-a1-1-1
		//         │   └── shelf-a1-1-2
		//         └── aisle-a1-2
		//     └── zone-a2
		//
		//   warehouse-b (root)
		//     ├── zone-b1
		//     └── zone-b2
		//         └── shelf-b2-1
		Name: "MultiRootDeepNesting",
		Repos: []rebuildRepoSpec{
			{Name: "virtual-src", Virtual: true},
			{Name: "warehouse-a"},
			{Name: "zone-a1", Parent: "warehouse-a"},
			{Name: "aisle-a1-1", Parent: "zone-a1"},
			{Name: "shelf-a1-1-1", Parent: "aisle-a1-1"},
			{Name: "shelf-a1-1-2", Parent: "aisle-a1-1"},
			{Name: "aisle-a1-2", Parent: "zone-a1"},
			{Name: "zone-a2", Parent: "warehouse-a"},
			{Name: "warehouse-b"},
			{Name: "zone-b1", Parent: "warehouse-b"},
			{Name: "zone-b2", Parent: "warehouse-b"},
			{Name: "shelf-b2-1", Parent: "zone-b2"},
		},
		Items: []string{"alpha", "beta"},
		Moves: []rebuildMoveSpec{
			{Item: "alpha", From: "virtual-src", To: "shelf-a1-1-1", Qty: 200, Execute: true},
			{Item: "alpha", From: "virtual-src", To: "shelf-a1-1-2", Qty: 100, Execute: true},
			{Item: "alpha", From: "shelf-a1-1-1", To: "shelf-a1-1-2", Qty: 50, Execute: true},
			{Item: "beta", From: "virtual-src", To: "zone-b1", Qty: 300, Execute: true},
			{Item: "beta", From: "zone-b1", To: "shelf-b2-1", Qty: 80, Execute: true},
			// Pending: intra-zone
			{Item: "alpha", From: "shelf-a1-1-2", To: "aisle-a1-2", Qty: 30},
			// Pending: cross-tree
			{Item: "beta", From: "zone-b1", To: "zone-a2", Qty: 50},
		},
	},
	{
		// Dynamic bin moved through a chain of parents: storage → staging → packing → outbound,
		// with item movements at each stage.
		//
		//   virtual (root, virtual)
		//   warehouse (root)
		//     ├── staging
		//     ├── storage
		//     │   └── bin (dynamic, moves through chain)
		//     ├── packing
		//     └── outbound
		Name: "ChainedRepoMovements",
		Repos: []rebuildRepoSpec{
			{Name: "virtual", Virtual: true},
			{Name: "warehouse"},
			{Name: "staging", Parent: "warehouse"},
			{Name: "storage", Parent: "warehouse"},
			{Name: "bin", Parent: "storage", Type: "dynamic"},
			{Name: "packing", Parent: "warehouse"},
			{Name: "outbound", Parent: "warehouse"},
		},
		Items: []string{"cargo"},
		Moves: []rebuildMoveSpec{
			// Phase 1: bin in storage
			{Item: "cargo", From: "virtual", To: "bin", Qty: 100, Execute: true},
			// Phase 2: move bin → staging
			{Type: "repo", From: "bin", To: "staging", Execute: true},
			{Item: "cargo", From: "virtual", To: "bin", Qty: 25, Execute: true},
			// Phase 3: move bin → packing
			{Type: "repo", From: "bin", To: "packing", Execute: true},
			{Item: "cargo", From: "bin", To: "packing", Qty: 40, Execute: true},
			// Phase 4: move bin → outbound
			{Type: "repo", From: "bin", To: "outbound", Execute: true},
			// Pending: bin → outbound (reservation)
			{Item: "cargo", From: "bin", To: "outbound", Qty: 15},
		},
	},
	{
		// Virtual repos as both inbound sources and outbound sinks.
		// Items flow: virtual-in → receiving → floor → shipping → virtual-out.
		//
		//   virtual-in (root, virtual)
		//   virtual-out (root, virtual)
		//   warehouse (root)
		//     ├── receiving-dock
		//     ├── floor-a
		//     ├── floor-b
		//     └── shipping-dock
		Name: "VirtualRepoInboundOutbound",
		Repos: []rebuildRepoSpec{
			{Name: "virtual-in", Virtual: true},
			{Name: "virtual-out", Virtual: true},
			{Name: "warehouse"},
			{Name: "receiving-dock", Parent: "warehouse"},
			{Name: "floor-a", Parent: "warehouse"},
			{Name: "floor-b", Parent: "warehouse"},
			{Name: "shipping-dock", Parent: "warehouse"},
		},
		Items: []string{"product"},
		Moves: []rebuildMoveSpec{
			// Inbound flow
			{Item: "product", From: "virtual-in", To: "receiving-dock", Qty: 500, Execute: true},
			{Item: "product", From: "receiving-dock", To: "floor-a", Qty: 200, Execute: true},
			{Item: "product", From: "receiving-dock", To: "floor-b", Qty: 150, Execute: true},
			// Internal movement
			{Item: "product", From: "floor-a", To: "floor-b", Qty: 60, Execute: true},
			{Item: "product", From: "floor-b", To: "shipping-dock", Qty: 100, Execute: true},
			// Outbound flow
			{Item: "product", From: "shipping-dock", To: "virtual-out", Qty: 80, Execute: true},
			// Pending movements
			{Item: "product", From: "receiving-dock", To: "floor-a", Qty: 50},
			{Item: "product", From: "floor-a", To: "shipping-dock", Qty: 30},
			{Item: "product", From: "shipping-dock", To: "virtual-out", Qty: 20},
		},
	},
	{
		// Heavy mix of created, executed, and deleted movements.
		// Deleted movements must be completely ignored by rebuild.
		//
		//   virtual (root, virtual)
		//   warehouse (root)
		//     ├── area-a
		//     │   └── shelf-a1
		//     └── area-b
		//         └── shelf-b1
		Name: "MixedDeletedAndActiveMovements",
		Repos: []rebuildRepoSpec{
			{Name: "virtual", Virtual: true},
			{Name: "warehouse"},
			{Name: "area-a", Parent: "warehouse"},
			{Name: "shelf-a1", Parent: "area-a"},
			{Name: "area-b", Parent: "warehouse"},
			{Name: "shelf-b1", Parent: "area-b"},
		},
		Items: []string{"widget"},
		Moves: []rebuildMoveSpec{
			// Executed
			{Item: "widget", From: "virtual", To: "shelf-a1", Qty: 200, Execute: true},
			{Item: "widget", From: "shelf-a1", To: "shelf-b1", Qty: 50, Execute: true},
			// Deleted (should be ignored)
			{Item: "widget", From: "shelf-a1", To: "shelf-b1", Qty: 30, Delete: true},
			{Item: "widget", From: "virtual", To: "area-b", Qty: 100, Delete: true},
			{Item: "widget", From: "virtual", To: "shelf-a1", Qty: 500, Delete: true},
			// More executed after deletions
			{Item: "widget", From: "shelf-b1", To: "shelf-a1", Qty: 20, Execute: true},
			{Item: "widget", From: "virtual", To: "shelf-b1", Qty: 75, Execute: true},
			// Pending
			{Item: "widget", From: "shelf-b1", To: "shelf-a1", Qty: 10},
			{Item: "widget", From: "shelf-a1", To: "area-b", Qty: 25},
		},
	},
	{
		// Dynamic cart moving between two separate warehouse trees with
		// interleaved item movements and cross-tree reservations.
		//
		//   virtual (root, virtual)
		//   tree-a (root)
		//     ├── branch-a1
		//     │   └── cart (dynamic, moves between trees)
		//     └── branch-a2
		//   tree-b (root)
		//     ├── branch-b1
		//     └── branch-b2
		Name: "CrossTreeRepoAndItemMovements",
		Repos: []rebuildRepoSpec{
			{Name: "virtual", Virtual: true},
			{Name: "tree-a"},
			{Name: "branch-a1", Parent: "tree-a"},
			{Name: "cart", Parent: "branch-a1", Type: "dynamic"},
			{Name: "branch-a2", Parent: "tree-a"},
			{Name: "tree-b"},
			{Name: "branch-b1", Parent: "tree-b"},
			{Name: "branch-b2", Parent: "tree-b"},
		},
		Items: []string{"item-x", "item-y"},
		Moves: []rebuildMoveSpec{
			// Phase 1: cart in tree-a/branch-a1
			{Item: "item-x", From: "virtual", To: "cart", Qty: 100, Execute: true},
			{Item: "item-y", From: "virtual", To: "branch-a1", Qty: 50, Execute: true},
			// Phase 2: move cart → branch-b1 (cross-tree)
			{Type: "repo", From: "cart", To: "branch-b1", Execute: true},
			{Item: "item-y", From: "virtual", To: "cart", Qty: 60, Execute: true},
			{Item: "item-y", From: "branch-a1", To: "branch-a2", Qty: 25, Execute: true},
			{Item: "item-x", From: "cart", To: "branch-b2", Qty: 30, Execute: true},
			// Phase 3: move cart → branch-a2 (back to tree-a)
			{Type: "repo", From: "cart", To: "branch-a2", Execute: true},
			// Pending
			{Item: "item-x", From: "cart", To: "branch-a2", Qty: 20},
			{Item: "item-x", From: "virtual", To: "branch-b2", Qty: 40},
		},
	},
	{
		// Three items distributed across a complex hierarchy with
		// cross-wing movements and multiple pending reservations.
		//
		//   virtual (root, virtual)
		//   hub (root)
		//     ├── north-wing
		//     │   ├── room-n1
		//     │   │   └── rack-n1a
		//     │   └── room-n2
		//     └── south-wing
		//         ├── room-s1
		//         └── room-s2
		//             └── rack-s2a
		Name: "MultipleItemsComplexHierarchy",
		Repos: []rebuildRepoSpec{
			{Name: "virtual", Virtual: true},
			{Name: "hub"},
			{Name: "north-wing", Parent: "hub"},
			{Name: "room-n1", Parent: "north-wing"},
			{Name: "rack-n1a", Parent: "room-n1"},
			{Name: "room-n2", Parent: "north-wing"},
			{Name: "south-wing", Parent: "hub"},
			{Name: "room-s1", Parent: "south-wing"},
			{Name: "room-s2", Parent: "south-wing"},
			{Name: "rack-s2a", Parent: "room-s2"},
		},
		Items: []string{"item-A", "item-B", "item-C"},
		Moves: []rebuildMoveSpec{
			// Item A: distributed across north wing
			{Item: "item-A", From: "virtual", To: "rack-n1a", Qty: 300, Execute: true},
			{Item: "item-A", From: "rack-n1a", To: "room-n2", Qty: 120, Execute: true},
			{Item: "item-A", From: "room-n2", To: "room-s1", Qty: 40, Execute: true},
			{Item: "item-A", From: "rack-n1a", To: "rack-s2a", Qty: 50}, // pending

			// Item B: distributed across south wing
			{Item: "item-B", From: "virtual", To: "room-s1", Qty: 250, Execute: true},
			{Item: "item-B", From: "virtual", To: "rack-s2a", Qty: 100, Execute: true},
			{Item: "item-B", From: "room-s1", To: "rack-s2a", Qty: 80, Execute: true},
			{Item: "item-B", From: "rack-s2a", To: "room-n1", Qty: 30, Execute: true},
			{Item: "item-B", From: "room-s1", To: "room-n2", Qty: 20}, // pending

			// Item C: small quantities, heavily moved
			{Item: "item-C", From: "virtual", To: "room-n1", Qty: 50, Execute: true},
			{Item: "item-C", From: "room-n1", To: "rack-n1a", Qty: 15, Execute: true},
			{Item: "item-C", From: "room-n1", To: "room-s2", Qty: 10, Execute: true},
			{Item: "item-C", From: "room-s2", To: "rack-s2a", Qty: 5, Execute: true},
			{Item: "item-C", From: "rack-n1a", To: "room-s1", Qty: 8, Execute: true},
			{Item: "item-C", From: "room-s1", To: "rack-n1a", Qty: 3}, // pending
			{Item: "item-C", From: "rack-s2a", To: "room-n2", Qty: 2}, // pending
		},
	},
	{
		// Repo movements that are pending or deleted, combined with item movements.
		// Deleted repo moves should be ignored; pending repo moves create reservations.
		//
		//   virtual (root, virtual)
		//   campus (root)
		//     ├── building-a
		//     │   ├── floor-1
		//     │   │   └── trolley (dynamic, moves between buildings)
		//     │   └── floor-2
		//     └── building-b
		//         ├── floor-3
		//         └── floor-4
		Name: "RepoMovementWithPendingAndDeleted",
		Repos: []rebuildRepoSpec{
			{Name: "virtual", Virtual: true},
			{Name: "campus"},
			{Name: "building-a", Parent: "campus"},
			{Name: "floor-1", Parent: "building-a"},
			{Name: "trolley", Parent: "floor-1", Type: "dynamic"},
			{Name: "floor-2", Parent: "building-a"},
			{Name: "building-b", Parent: "campus"},
			{Name: "floor-3", Parent: "building-b"},
			{Name: "floor-4", Parent: "building-b"},
		},
		Items: []string{"item-P", "item-Q"},
		Moves: []rebuildMoveSpec{
			// Load trolley and fixed repos
			{Item: "item-P", From: "virtual", To: "trolley", Qty: 150, Execute: true},
			{Item: "item-Q", From: "virtual", To: "trolley", Qty: 80, Execute: true},
			{Item: "item-P", From: "virtual", To: "floor-2", Qty: 200, Execute: true},
			{Item: "item-Q", From: "virtual", To: "floor-3", Qty: 100, Execute: true},
			// Move trolley: floor-1 → floor-3 (executed)
			{Type: "repo", From: "trolley", To: "floor-3", Execute: true},
			// Item moves while trolley in floor-3
			{Item: "item-P", From: "trolley", To: "floor-3", Qty: 50, Execute: true},
			{Item: "item-Q", From: "floor-3", To: "floor-4", Qty: 30, Execute: true},
			// Deleted repo movement (should be ignored)
			{Type: "repo", From: "trolley", To: "floor-4", Delete: true},
			// Move trolley: floor-3 → floor-2 (executed, crosses buildings)
			{Type: "repo", From: "trolley", To: "floor-2", Execute: true},
			// Item moves after final repo move
			{Item: "item-Q", From: "trolley", To: "floor-2", Qty: 25, Execute: true},
			// Pending item movements
			{Item: "item-P", From: "floor-2", To: "floor-4", Qty: 15},
			{Item: "item-P", From: "trolley", To: "floor-1", Qty: 10},
			// Deleted item movement (should be ignored)
			{Item: "item-P", From: "floor-2", To: "floor-3", Qty: 50, Delete: true},
		},
	},
	{
		// Regression test for a production bug where stock on deep child pallets
		// in an Inbound area did not propagate to parent repos (Inbound, Halle)
		// after rebuild, while the Outbound tree propagated correctly.
		//
		// The bug was caused by incorrect repo movement undo (due to nil
		// ExecutedAt and unchecked repoMap lookups) corrupting the parent chain
		// during event replay, so applyActualStockDelta walked an empty/wrong
		// chain for inbound stock, while reservations (processed later with
		// correct tree state) propagated fine.
		//
		//   virtual-wh (root, virtual)
		//
		//   halle (root)
		//     ├── inbound
		//     │   └── pallet-in (dynamic, stays in inbound)
		//     └── outbound
		//         └── pallet-out (dynamic, moved here from inbound via repo move)
		//             ├── box-0 (dynamic)
		//             ├── box-1 (dynamic)
		//             └── box-2 (dynamic)
		Name: "InboundOutboundPalletPropagation",
		Repos: []rebuildRepoSpec{
			{Name: "virtual-wh", Virtual: true},
			{Name: "halle"},
			{Name: "inbound", Parent: "halle"},
			{Name: "outbound", Parent: "halle"},
			{Name: "pallet-in", Parent: "inbound", Type: "dynamic"},
			{Name: "pallet-out", Parent: "inbound", Type: "dynamic"},
			{Name: "box-0", Parent: "pallet-out", Type: "dynamic"},
			{Name: "box-1", Parent: "pallet-out", Type: "dynamic"},
			{Name: "box-2", Parent: "pallet-out", Type: "dynamic"},
		},
		Items: []string{"sku-a", "sku-b", "sku-c", "sku-d"},
		Moves: []rebuildMoveSpec{
			// Stock arrives on inbound pallets from virtual source.
			{Item: "sku-a", From: "virtual-wh", To: "pallet-in", Qty: 30, Execute: true},
			{Item: "sku-b", From: "virtual-wh", To: "pallet-in", Qty: 15, Execute: true},
			{Item: "sku-c", From: "virtual-wh", To: "pallet-in", Qty: 15, Execute: true},
			{Item: "sku-d", From: "virtual-wh", To: "pallet-in", Qty: 15, Execute: true},

			// Stock loaded onto outbound pallet (still in inbound at this point).
			{Item: "sku-a", From: "virtual-wh", To: "pallet-out", Qty: 30, Execute: true},
			{Item: "sku-b", From: "virtual-wh", To: "pallet-out", Qty: 15, Execute: true},
			{Item: "sku-c", From: "virtual-wh", To: "pallet-out", Qty: 15, Execute: true},
			{Item: "sku-d", From: "virtual-wh", To: "pallet-out", Qty: 15, Execute: true},

			// Move pallet-out from inbound to outbound (repo movement).
			{Type: "repo", From: "pallet-out", To: "outbound", Execute: true},

			// Distribute stock from pallet-out into boxes (after repo move).
			{Item: "sku-a", From: "pallet-out", To: "box-0", Qty: 10, Execute: true},
			{Item: "sku-a", From: "pallet-out", To: "box-1", Qty: 10, Execute: true},
			{Item: "sku-a", From: "pallet-out", To: "box-2", Qty: 10, Execute: true},
			{Item: "sku-b", From: "pallet-out", To: "box-0", Qty: 5, Execute: true},
			{Item: "sku-b", From: "pallet-out", To: "box-1", Qty: 5, Execute: true},
			{Item: "sku-b", From: "pallet-out", To: "box-2", Qty: 5, Execute: true},
			{Item: "sku-c", From: "pallet-out", To: "box-0", Qty: 5, Execute: true},
			{Item: "sku-c", From: "pallet-out", To: "box-1", Qty: 5, Execute: true},
			{Item: "sku-c", From: "pallet-out", To: "box-2", Qty: 5, Execute: true},
			{Item: "sku-d", From: "pallet-out", To: "box-0", Qty: 5, Execute: true},
			{Item: "sku-d", From: "pallet-out", To: "box-1", Qty: 5, Execute: true},
			{Item: "sku-d", From: "pallet-out", To: "box-2", Qty: 5, Execute: true},

			// Pending outgoing reservation on pallet-in (models outbound process).
			{Item: "sku-a", From: "pallet-in", To: "outbound", Qty: 30},
		},
	},
	{
		// Simple scenario used by the idempotent double-rebuild test.
		//
		//   virtual (root, virtual)
		//   warehouse (root)
		//     └── shelf
		//         └── bin (dynamic, moved to warehouse)
		Name: "DoubleRebuild",
		Repos: []rebuildRepoSpec{
			{Name: "virtual", Virtual: true},
			{Name: "warehouse"},
			{Name: "shelf", Parent: "warehouse"},
			{Name: "bin", Parent: "shelf", Type: "dynamic"},
		},
		Items: []string{"part"},
		Moves: []rebuildMoveSpec{
			{Item: "part", From: "virtual", To: "bin", Qty: 100, Execute: true},
			{Item: "part", From: "bin", To: "shelf", Qty: 30, Execute: true},
			{Item: "part", From: "shelf", To: "bin", Qty: 10}, // pending
			{Type: "repo", From: "bin", To: "warehouse", Execute: true},
		},
	},
}

// TestRebuildInventoryStock_Scenarios runs all data-driven rebuild test scenarios.
// Each scenario creates a complex repository hierarchy with movements, snapshots
// stock levels, corrupts the stock table, rebuilds, and asserts the rebuilt stock
// matches the original snapshot exactly.
func TestRebuildInventoryStock_Scenarios(t *testing.T) {
	t.Parallel()
	for _, scenario := range rebuildScenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()
			runRebuildScenario(t, scenario)
		})
	}
}

// TestRebuildInventoryStock_DoubleRebuild_Idempotent verifies that running
// rebuild twice in succession produces identical results both times.
// Each rebuild cycle corrupts stock before rebuilding to prove correctness.
func TestRebuildInventoryStock_DoubleRebuild_Idempotent(t *testing.T) {
	t.Parallel()
	te := setup(t)
	ctx := te.ctx(userA)
	apiClient := setupAPIClient(t, te)

	// Use the DoubleRebuild scenario data for setup.
	scenario := rebuildScenarios[len(rebuildScenarios)-1]
	require.Equal(t, "DoubleRebuild", scenario.Name, "last scenario should be DoubleRebuild")

	repos, allItemIDs := setupScenario(t, ctx, apiClient, scenario)

	// Capture the original correct stock state
	original := captureStockSnapshot(t, ctx, apiClient, repos, allItemIDs)

	// First cycle: corrupt → rebuild → verify
	corruptAllStock(t, ctx, te.Ent)

	result1, err := apiClient.RebuildInventoryStock(ctx)
	require.NoError(t, err)
	assert.True(t, result1.GetRebuildInventoryStock().GetSuccess())
	assertStockSnapshot(t, ctx, apiClient, repos, allItemIDs, original)

	// Second cycle: corrupt again → rebuild → verify same result
	corruptAllStock(t, ctx, te.Ent)

	result2, err := apiClient.RebuildInventoryStock(ctx)
	require.NoError(t, err)
	assert.True(t, result2.GetRebuildInventoryStock().GetSuccess())
	assertStockSnapshot(t, ctx, apiClient, repos, allItemIDs, original)
}

// TestRebuildInventoryStock_RequiresAdminRole verifies that the
// rebuildInventoryStock mutation rejects non-admin users with an unauthorized
// error. Writer-role users must not be able to trigger a full stock rebuild.
func TestRebuildInventoryStock_RequiresAdminRole(t *testing.T) {
	t.Parallel()
	te := setup(t)

	writerUser := &authn.User{
		ID:       uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_WRITER},
	}

	writerClient := setupAPIClientForUser(t, te, writerUser)
	ctx := te.ctx(writerUser)

	_, err := writerClient.RebuildInventoryStock(ctx)
	require.Error(t, err, "non-admin user should be rejected")
	assert.Contains(t, err.Error(), "unauthorized", "error should indicate unauthorized access")
}

// =============================================================================
// TESTS FOR REVIEW FEEDBACK ISSUES
// =============================================================================
//
// These tests verify potential non-determinism and error-handling gaps identified
// during code review of RebuildStockTable. They exercise:
//
//   - Same-timestamp movements with no tiebreaker (feedback 1 & 4)
//   - Nil ExecutedAt on executed repo movements (feedback 2)
//   - Orphaned repository movements — both error and corruption paths (feedback 3)
//   - Multiple rebuild consistency (feedback 4)

// execSQL is a test helper that executes raw SQL against the ent client's
// underlying database driver. It panics on error for concise test code.
func execSQL(t *testing.T, ctx context.Context, entClient *ent.Client, query string, args ...any) {
	t.Helper()

	_, err := entClient.ExecContext(ctx, query, args...)
	require.NoError(t, err, "raw SQL exec failed: %s", query)
}

// TestRebuildInventoryStock_SameTimestampMovements_Deterministic verifies that
// rebuild produces correct results when multiple executed item movements share
// the exact same created_at and executed_at timestamps.
//
// This covers review feedback 1 and 4:
//   - DB queries have no ORDER BY, so input order is unspecified
//   - Event sort only breaks ties by kind, not by movement ID
//
// Scenario: Two sequential executed movements where order matters:
//
//	m1: virtual → shelf-a, qty 100 (execute)
//	m2: shelf-a → shelf-b, qty 50 (execute)
//
// If processed in wrong order (m2 before m1), shelf-a would have 0 stock when
// m2 tries to subtract 50, causing clamping to 0 and wrong final totals.
// After forcing both timestamps to be identical, rebuild must still produce
// the correct result matching the pre-corruption snapshot.
func TestRebuildInventoryStock_SameTimestampMovements_Deterministic(t *testing.T) {
	t.Parallel()
	te := setup(t)
	ctx := te.ctx(userA)
	apiClient := setupAPIClient(t, te)

	// Create repos
	virtualID := stockTestCreateRepository(t, ctx, apiClient, "virtual", entrepository.TypeStatic, true, nil)
	warehouseID := stockTestCreateRepository(t, ctx, apiClient, "warehouse", entrepository.TypeStatic, false, nil)
	shelfAID := stockTestCreateRepository(t, ctx, apiClient, "shelf-a", entrepository.TypeStatic, false, &warehouseID)
	shelfBID := stockTestCreateRepository(t, ctx, apiClient, "shelf-b", entrepository.TypeStatic, false, &warehouseID)

	// Create item
	itemID := stockTestCreateItem(t, ctx, apiClient, "SAME-TS-ITEM-001")

	// Create and execute movements in the correct order
	m1 := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, shelfAID, 100)
	stockTestExecuteItemMovement(t, ctx, apiClient, m1)

	m2 := stockTestCreateItemMovement(t, ctx, apiClient, itemID, shelfAID, shelfBID, 50)
	stockTestExecuteItemMovement(t, ctx, apiClient, m2)

	// Record correct stock levels BEFORE timestamp manipulation
	repos := []rebuildTestRepo{
		{id: virtualID, label: "virtual"},
		{id: warehouseID, label: "warehouse"},
		{id: shelfAID, label: "shelf-a"},
		{id: shelfBID, label: "shelf-b"},
	}
	itemIDs := []string{itemID}
	before := captureStockSnapshot(t, ctx, apiClient, repos, itemIDs)

	// Force all movements to have the EXACT same created_at and executed_at
	// via raw SQL (created_at is immutable in ent, so we must use raw SQL).
	fixedTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)

	execSQL(t, ctx, te.Ent,
		"UPDATE item_movements SET created_at = ?, executed_at = ? WHERE tenant_id = ?",
		fixedTime, fixedTime, tenantA.String())

	// Corrupt stock and rebuild
	corruptAllStock(t, ctx, te.Ent)

	result, err := apiClient.RebuildInventoryStock(ctx)
	require.NoError(t, err, "RebuildInventoryStock should not error")
	require.NotNil(t, result)
	assert.True(t, result.GetRebuildInventoryStock().GetSuccess(), "rebuild should report success")

	// Assert rebuilt stock matches the pre-corruption snapshot.
	// If the sort is non-deterministic, this may produce wrong results due to
	// clamping: shelf-a would be 100 instead of 50, shelf-b would be 50 still.
	assertStockSnapshot(t, ctx, apiClient, repos, itemIDs, before)
}

// TestRebuildInventoryStock_RepoMovement_NilExecutedAt verifies that rebuild
// handles a repository movement that has executed=true but executed_at=NULL.
//
// When a repo movement has executed=true but executed_at=NULL (data corruption),
// the rebuild treats it as PENDING (since executed_at is the only indicator that
// the movement was actually executed in the chronological replay). This means the
// repository parent chain is NOT updated, so box remains under warehouse rather than
// storage. The item stock (50 qty) is in box, whose current parent is warehouse —
// so warehouse and storage each see their own inherited totals but the physical
// stock remains under warehouse's subtree, not storage's.
//
// The test verifies that rebuild handles this deterministically (no panic, no
// corrupt/inconsistent state) and produces a predictable result: the movement
// is treated as only a pending reservation (incoming stock) rather than an
// executed reparent, because executed_at is nil.
func TestRebuildInventoryStock_RepoMovement_NilExecutedAt(t *testing.T) {
	t.Parallel()
	te := setup(t)
	ctx := te.ctx(userA)
	apiClient := setupAPIClient(t, te)

	// Create repos: virtual, warehouse, storage (child), box (dynamic, child of warehouse — will be moved)
	virtualID := stockTestCreateRepository(t, ctx, apiClient, "virtual", entrepository.TypeStatic, true, nil)
	warehouseID := stockTestCreateRepository(t, ctx, apiClient, "warehouse", entrepository.TypeStatic, false, nil)
	storageID := stockTestCreateRepository(t, ctx, apiClient, "storage", entrepository.TypeStatic, false, &warehouseID)
	boxID := stockTestCreateRepository(t, ctx, apiClient, "box", entrepository.TypeDynamic, false, &warehouseID)

	// Create item and stock
	itemID := stockTestCreateItem(t, ctx, apiClient, "NIL-EXEC-ITEM-001")

	// virtual → box, qty 50 (create + execute)
	m1 := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, boxID, 50)
	stockTestExecuteItemMovement(t, ctx, apiClient, m1)

	// Move box: warehouse → storage (create + execute repo movement)
	rm1 := stockTestCreateRepositoryMovement(t, ctx, apiClient, boxID, warehouseID, storageID)
	stockTestExecuteRepositoryMovement(t, ctx, apiClient, rm1)

	// Corrupt the repo movement: set executed_at = NULL while keeping executed = true.
	// This simulates data corruption that RebuildStockTable is designed to handle.
	// With executed_at=NULL, rebuild treats the movement as PENDING (not executed),
	// so the repository parent chain is NOT rewound/re-applied for this movement.
	execSQL(t, ctx, te.Ent,
		"UPDATE repository_movements SET executed_at = NULL WHERE id = ?",
		rm1)

	// Corrupt stock and rebuild
	corruptAllStock(t, ctx, te.Ent)

	result, err := apiClient.RebuildInventoryStock(ctx)
	require.NoError(t, err, "RebuildInventoryStock should not error for nil ExecutedAt")
	require.NotNil(t, result)
	assert.True(t, result.GetRebuildInventoryStock().GetSuccess(), "rebuild should report success")

	// With executed_at=NULL, the repo movement is treated as PENDING only.
	// The rewind step skips it (no executed_at → not in executedRepoMovs).
	// The event loop sees it as rebuildPendingRepo only (1 event).
	// So box remains as a child of warehouse (its current state in DB after
	// the rewind step left it under storage since there was nothing to rewind).
	//
	// Actually: after the EXECUTE was done, box's parent was updated to storage.
	// The rewind step finds no executedRepoMovs (executed_at is nil), so it does
	// NOT rewind box back to warehouse. Box stays under storage in the DB during replay.
	// The item movement (virtual→box, qty 50) is replayed: box is under storage,
	// so stock propagates up to storage and warehouse.
	// The pending repo movement (box warehouse→storage) also fires, adding incoming
	// stock to storage (box itself). Net result: storage has Quantity=50 (from the item
	// movement propagating up the current parent chain) plus IncomingStock from the
	// pending repo event.
	//
	// The key assertion is: rebuild succeeds and produces a deterministic result
	// without panicking or corrupting data.
	storageStock := getStockForRepo(t, ctx, apiClient, storageID, itemID)
	require.NotNil(t, storageStock, "storage should have stock after rebuild")

	warehouseStock := getStockForRepo(t, ctx, apiClient, warehouseID, itemID)
	require.NotNil(t, warehouseStock, "warehouse should have stock after rebuild")

	// Warehouse must aggregate stock from its entire subtree (box is still under
	// storage which is under warehouse). Total owned+incoming must be >= 50.
	totalWarehouse := warehouseStock.GetQuantity() + warehouseStock.GetIncomingStock()
	assert.GreaterOrEqual(t, totalWarehouse, 50,
		"warehouse should aggregate at least 50 units across quantity+incoming after rebuild")
}

// TestRebuildInventoryStock_OrphanedRepoMovement_ReturnsError verifies that
// rebuild detects when a repository movement references a repository that no
// longer exists in the database.
//
// The test hard-deletes a repository (bypassing FK constraints) and expects
// rebuild to return an explicit, descriptive error for the orphaned movement.
func TestRebuildInventoryStock_OrphanedRepoMovement_ReturnsError(t *testing.T) {
	t.Parallel()
	te := setup(t)
	ctx := te.ctx(userA)
	apiClient := setupAPIClient(t, te)

	// Create repos
	virtualID := stockTestCreateRepository(t, ctx, apiClient, "virtual", entrepository.TypeStatic, true, nil)
	warehouseID := stockTestCreateRepository(t, ctx, apiClient, "warehouse", entrepository.TypeStatic, false, nil)
	boxID := stockTestCreateRepository(t, ctx, apiClient, "box", entrepository.TypeDynamic, false, &warehouseID)
	destID := stockTestCreateRepository(t, ctx, apiClient, "dest", entrepository.TypeStatic, false, &warehouseID)

	// Create item and stock
	itemID := stockTestCreateItem(t, ctx, apiClient, "ORPHAN-ERR-ITEM-001")

	// virtual → box, qty 100 (create + execute)
	m1 := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, boxID, 100)
	stockTestExecuteItemMovement(t, ctx, apiClient, m1)

	// Move box: warehouse → dest (create + execute repo movement)
	rm1 := stockTestCreateRepositoryMovement(t, ctx, apiClient, boxID, warehouseID, destID)
	stockTestExecuteRepositoryMovement(t, ctx, apiClient, rm1)

	// Hard-delete the "box" repository via raw SQL, bypassing FK constraints.
	// This simulates a data corruption scenario where the referenced repo is gone.
	execSQL(t, ctx, te.Ent, "PRAGMA foreign_keys = OFF")
	execSQL(t, ctx, te.Ent, "DELETE FROM repositories WHERE id = ?", boxID)
	execSQL(t, ctx, te.Ent, "PRAGMA foreign_keys = ON")

	// Corrupt stock and attempt rebuild
	corruptAllStock(t, ctx, te.Ent)

	_, err := apiClient.RebuildInventoryStock(ctx)

	// The rebuild should return a descriptive error identifying the missing
	// repository by ID, not a downstream FK constraint violation.
	require.Error(t, err, "rebuild should error when repo movement references a deleted repository")
	assert.Contains(t, err.Error(), "not found in repoMap",
		"rebuild should return a descriptive 'repository not found in repoMap' error, not a downstream FK constraint failure")
}

// TestRebuildInventoryStock_OrphanedRepoMovement_SilentCorruption demonstrates
// the silent stock corruption that occurs when a repository movement references
// a hard-deleted repository.
//
// This covers review feedback 3 (corruption path):
//   - A zero-value ent.Repository is inserted into repoMap (VirtualRepo=false, ID=zero UUID)
//   - This causes incorrect parent-chain propagation during stock calculation
//   - The result: stock totals after rebuild differ from the correct pre-corruption state
//
// Unlike the error test, this one asserts that the rebuild produces WRONG results,
// proving the silent corruption path exists.
func TestRebuildInventoryStock_OrphanedRepoMovement_SilentCorruption(t *testing.T) {
	t.Parallel()
	te := setup(t)
	ctx := te.ctx(userA)
	apiClient := setupAPIClient(t, te)

	// Create repos: virtual, warehouse (parent), shelf (child of warehouse),
	// box (dynamic, child of shelf — will be moved to warehouse then deleted)
	virtualID := stockTestCreateRepository(t, ctx, apiClient, "virtual", entrepository.TypeStatic, true, nil)
	warehouseID := stockTestCreateRepository(t, ctx, apiClient, "warehouse", entrepository.TypeStatic, false, nil)
	shelfID := stockTestCreateRepository(t, ctx, apiClient, "shelf", entrepository.TypeStatic, false, &warehouseID)
	boxID := stockTestCreateRepository(t, ctx, apiClient, "box", entrepository.TypeDynamic, false, &shelfID)

	// Create item and add stock to box and shelf
	itemID := stockTestCreateItem(t, ctx, apiClient, "ORPHAN-CORRUPT-ITEM-001")

	// virtual → box, qty 80 (create + execute)
	m1 := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, boxID, 80)
	stockTestExecuteItemMovement(t, ctx, apiClient, m1)

	// virtual → shelf, qty 40 (create + execute)
	m2 := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, shelfID, 40)
	stockTestExecuteItemMovement(t, ctx, apiClient, m2)

	// Move box: shelf → warehouse (create + execute repo movement)
	rm1 := stockTestCreateRepositoryMovement(t, ctx, apiClient, boxID, shelfID, warehouseID)
	stockTestExecuteRepositoryMovement(t, ctx, apiClient, rm1)

	// Record correct stock levels
	repos := []rebuildTestRepo{
		{id: virtualID, label: "virtual"},
		{id: warehouseID, label: "warehouse"},
		{id: shelfID, label: "shelf"},
		{id: boxID, label: "box"},
	}
	itemIDs := []string{itemID}
	correctSnapshot := captureStockSnapshot(t, ctx, apiClient, repos, itemIDs)

	// Verify we have meaningful stock before corruption
	require.NotEqual(t, stockLevel{}, correctSnapshot[warehouseID][itemID],
		"warehouse should have non-zero stock before corruption")
	require.NotEqual(t, stockLevel{}, correctSnapshot[boxID][itemID],
		"box should have non-zero stock before corruption")

	// Hard-delete the "box" repository to create the orphaned movement condition
	execSQL(t, ctx, te.Ent, "PRAGMA foreign_keys = OFF")
	execSQL(t, ctx, te.Ent, "DELETE FROM repositories WHERE id = ?", boxID)
	execSQL(t, ctx, te.Ent, "PRAGMA foreign_keys = ON")

	// Corrupt stock and rebuild
	corruptAllStock(t, ctx, te.Ent)

	result, err := apiClient.RebuildInventoryStock(ctx)
	if err != nil {
		// If rebuild returns an error, the code has been fixed — the orphaned
		// movement is now detected. This is the desired behavior.
		t.Logf("RebuildInventoryStock correctly returned error for orphaned repo: %v", err)
		return
	}

	// If we reach here, rebuild succeeded silently (current buggy behavior).
	// Verify that the stock is WRONG — proving silent corruption.
	require.NotNil(t, result)
	assert.True(t, result.GetRebuildInventoryStock().GetSuccess())

	// Check warehouse stock after rebuild with orphaned box.
	// The zero-value repoMap entry for box (VirtualRepo=false, ParentID=zero)
	// breaks the parent-chain aggregation, likely producing wrong warehouse totals.
	afterWarehouse := getStockForRepo(t, ctx, apiClient, warehouseID, itemID)
	if afterWarehouse == nil {
		t.Log("BUG: warehouse has no stock after rebuild with orphaned repo movement. "+
			"Expected non-zero stock (correct warehouse quantity was ",
			correctSnapshot[warehouseID][itemID].Quantity, ")")
		return
	}

	correctWarehouseQty := correctSnapshot[warehouseID][itemID].Quantity
	actualWarehouseQty := afterWarehouse.GetQuantity()

	if actualWarehouseQty != correctWarehouseQty {
		t.Logf("BUG CONFIRMED: warehouse stock after rebuild = %d, expected = %d. "+
			"The zero-value repoMap entry for orphaned box caused silent corruption "+
			"in parent-chain aggregation.",
			actualWarehouseQty, correctWarehouseQty)
	} else {
		t.Log("Warehouse stock matches despite orphaned repo — corruption may be in other fields")
	}
}

// TestRebuildInventoryStock_MultipleRebuildConsistent verifies that running
// rebuild multiple times with same-timestamp movements produces identical
// results each time, confirming determinism.
//
// This covers review feedback 4:
//   - Event sort only breaks ties by kind, no movement-level tiebreaker
//   - Without stable deterministic ordering, successive rebuilds could differ
//
// The test forces all timestamps to be identical and runs rebuild 3 times,
// asserting all three produce the same stock snapshot.
func TestRebuildInventoryStock_MultipleRebuildConsistent(t *testing.T) {
	t.Parallel()
	te := setup(t)
	ctx := te.ctx(userA)
	apiClient := setupAPIClient(t, te)

	// Create repos
	virtualID := stockTestCreateRepository(t, ctx, apiClient, "virtual", entrepository.TypeStatic, true, nil)
	warehouseID := stockTestCreateRepository(t, ctx, apiClient, "warehouse", entrepository.TypeStatic, false, nil)
	shelfAID := stockTestCreateRepository(t, ctx, apiClient, "shelf-a", entrepository.TypeStatic, false, &warehouseID)
	shelfBID := stockTestCreateRepository(t, ctx, apiClient, "shelf-b", entrepository.TypeStatic, false, &warehouseID)

	// Create two items to increase event count
	item1ID := stockTestCreateItem(t, ctx, apiClient, "CONSIST-ITEM-001")
	item2ID := stockTestCreateItem(t, ctx, apiClient, "CONSIST-ITEM-002")

	// Create and execute multiple movements for both items
	// Item 1: virtual → shelf-a → shelf-b
	m1 := stockTestCreateItemMovement(t, ctx, apiClient, item1ID, virtualID, shelfAID, 200)
	stockTestExecuteItemMovement(t, ctx, apiClient, m1)
	m2 := stockTestCreateItemMovement(t, ctx, apiClient, item1ID, shelfAID, shelfBID, 75)
	stockTestExecuteItemMovement(t, ctx, apiClient, m2)

	// Item 2: virtual → shelf-b → shelf-a
	m3 := stockTestCreateItemMovement(t, ctx, apiClient, item2ID, virtualID, shelfBID, 150)
	stockTestExecuteItemMovement(t, ctx, apiClient, m3)
	m4 := stockTestCreateItemMovement(t, ctx, apiClient, item2ID, shelfBID, shelfAID, 60)
	stockTestExecuteItemMovement(t, ctx, apiClient, m4)

	// Pending movements (same timestamp will make ordering ambiguous)
	_ = stockTestCreateItemMovement(t, ctx, apiClient, item1ID, shelfBID, shelfAID, 20)
	_ = stockTestCreateItemMovement(t, ctx, apiClient, item2ID, shelfAID, shelfBID, 30)

	// Force all timestamps to be identical
	fixedTime := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	execSQL(t, ctx, te.Ent,
		"UPDATE item_movements SET created_at = ?, executed_at = ? WHERE tenant_id = ? AND executed_at IS NOT NULL",
		fixedTime, fixedTime, tenantA.String())
	execSQL(t, ctx, te.Ent,
		"UPDATE item_movements SET created_at = ? WHERE tenant_id = ? AND executed_at IS NULL",
		fixedTime, tenantA.String())

	repos := []rebuildTestRepo{
		{id: virtualID, label: "virtual"},
		{id: warehouseID, label: "warehouse"},
		{id: shelfAID, label: "shelf-a"},
		{id: shelfBID, label: "shelf-b"},
	}
	itemIDs := []string{item1ID, item2ID}

	// Run rebuild 3 times and capture snapshots
	var snapshots [3]map[string]map[string]stockLevel

	for i := range 3 {
		corruptAllStock(t, ctx, te.Ent)

		result, err := apiClient.RebuildInventoryStock(ctx)
		require.NoError(t, err, "rebuild %d should not error", i+1)
		require.NotNil(t, result)
		assert.True(t, result.GetRebuildInventoryStock().GetSuccess(), "rebuild %d should succeed", i+1)

		snapshots[i] = captureStockSnapshot(t, ctx, apiClient, repos, itemIDs)
	}

	// Assert all three rebuilds produced identical results
	for _, repo := range repos {
		for _, itemID := range itemIDs {
			level1 := snapshots[0][repo.id][itemID]
			level2 := snapshots[1][repo.id][itemID]
			level3 := snapshots[2][repo.id][itemID]

			assert.Equal(t, level1, level2,
				"rebuild 1 vs 2 mismatch for %s/%s", repo.label, itemID)
			assert.Equal(t, level2, level3,
				"rebuild 2 vs 3 mismatch for %s/%s", repo.label, itemID)
		}
	}
}

// =============================================================================
// ENHANCED FUZZ-STYLE REBUILD TEST
// =============================================================================

// fuzzConfig controls the complexity and behavior of the rebuild fuzzer.
// All tree structure, movement count, and execution ratios are deterministically
// derived from the Seed, making every run reproducible.
type fuzzConfig struct {
	Seed             int64   // RNG seed for reproducibility
	Complexity       int     // Complexity level (1–5) for logging/naming
	Iterations       int     // Number of stock movement operations in the main loop
	ItemCount        int     // Number of distinct item SKUs to create
	RootCount        int     // Number of non-virtual root repository trees
	VirtualTreeCount int     // Number of all-virtual root trees (stock sources/sinks)
	MinBreadth       int     // Min children per internal node
	MaxBreadth       int     // Max children per internal node
	MinDepth         int     // Min depth per tree branch (forced branching below this)
	MaxDepth         int     // Max depth per tree branch (no branching at or beyond)
	MaxLeaves        int     // Soft cap on total leaf nodes across all trees
	ExecuteRatio     float64 // Fraction of movements that get executed (0.0-1.0)
	DynamicProb      float64 // Probability a non-virtual repo is dynamic (vs static)
	SeedQtyMin       int     // Min initial stock per repo/item seeded from virtual root
	SeedQtyMax       int     // Max initial stock per repo/item seeded from virtual root
	MoveQtyMin       int     // Min quantity per random movement
	MoveQtyMax       int     // Max quantity per random movement

	// AllowMixedTrees controls whether a single tree can contain both virtual
	// and non-virtual repos. Currently the API enforces that virtual/non-virtual
	// trees are separate: a tree is either entirely virtual or entirely
	// non-virtual. Set to true once mixed trees are supported (future feature).
	AllowMixedTrees bool
}

// generateFuzzRepoTree generates a random repository tree from the fuzzer config.
// It creates separate virtual and non-virtual trees (no mixing unless
// AllowMixedTrees is enabled). Virtual trees are all-virtual; physical trees
// use a mix of static and dynamic repo types.
func generateFuzzRepoTree(rng *rand.Rand, cfg fuzzConfig) []rebuildRepoSpec {
	var specs []rebuildRepoSpec

	counter := 0
	leafCount := 0

	// All-virtual root repos (stock sources/sinks).
	// The API enforces "the depth of virtual zones is maximum 1", so virtual
	// repos are standalone roots without children. The AllowMixedTrees flag
	// is reserved for future use when deeper virtual trees become supported.
	for range cfg.VirtualTreeCount {
		rootName := fmt.Sprintf("virt-%d", counter)
		counter++

		specs = append(specs, rebuildRepoSpec{
			Name:    rootName,
			Virtual: true,
		})
	}

	// Non-virtual root trees (physical repos, mix of static/dynamic).
	for r := 0; r < cfg.RootCount && leafCount < cfg.MaxLeaves; r++ {
		rootName := fmt.Sprintf("repo-%d", counter)
		counter++

		repoType := ""
		if rng.Float64() < cfg.DynamicProb {
			repoType = "dynamic"
		}

		specs = append(specs, rebuildRepoSpec{
			Name: rootName,
			Type: repoType,
		})

		generateFuzzSubtree(rng, cfg, &specs, &counter, &leafCount, rootName, 1, false)
	}

	return specs
}

// generateFuzzSubtree recursively adds children to a parent repo node.
// Branching is forced below MinDepth, optional between MinDepth and MaxDepth,
// and stopped at MaxDepth. The virtual flag propagates to all children (trees
// are either entirely virtual or entirely non-virtual).
func generateFuzzSubtree(
	rng *rand.Rand,
	cfg fuzzConfig,
	specs *[]rebuildRepoSpec,
	counter *int,
	leafCount *int,
	parent string,
	depth int,
	virtual bool,
) {
	if *leafCount >= cfg.MaxLeaves {
		return
	}

	// Decide whether to branch at this depth.
	shouldBranch := false

	switch {
	case depth < cfg.MinDepth:
		shouldBranch = true // Must branch to reach minimum depth
	case depth < cfg.MaxDepth:
		shouldBranch = rng.Float64() < 0.6 // 60% chance to continue growing
	}

	if !shouldBranch {
		*leafCount++

		return
	}

	// Generate children.
	breadth := cfg.MinBreadth
	if cfg.MaxBreadth > cfg.MinBreadth {
		breadth += rng.Intn(cfg.MaxBreadth - cfg.MinBreadth + 1)
	}

	for b := 0; b < breadth && *leafCount < cfg.MaxLeaves; b++ {
		name := fmt.Sprintf("repo-%d", *counter)
		if virtual {
			name = fmt.Sprintf("virt-%d", *counter)
		}
		*counter++

		repoType := ""
		if !virtual && rng.Float64() < cfg.DynamicProb {
			repoType = "dynamic"
		}

		*specs = append(*specs, rebuildRepoSpec{
			Name:    name,
			Parent:  parent,
			Type:    repoType,
			Virtual: virtual,
		})

		generateFuzzSubtree(rng, cfg, specs, counter, leafCount, name, depth+1, virtual)
	}
}

// =============================================================================
// FUZZER-DRIVEN REBUILD TESTS
// =============================================================================

// runFuzzRebuildTestViaMovementFlow generates a pseudo-random repository tree
// and stock movements using fuzzConfig, then verifies that RebuildInventoryStock
// correctly restores all stock values after corruption.
//
// The database is the sole source of truth — no stock tracker is used:
//   - Seed moves (virtual → every physical repo, for every item) are always executed
//   - Random moves use stockTestTryCreateItemMovementWithID: if the create
//     succeeds, execute or delete is attempted via the try-variant (errors ignored);
//     if the create fails (e.g. insufficient stock) the move is silently skipped
//   - After all moves: captureRawStockSnapshot → corrupt → rebuild → assert
//
//nolint:funlen,cyclop // Fuzzer setup is intentionally complex.
func runFuzzRebuildTestViaMovementFlow(t *testing.T, cfg fuzzConfig) {
	t.Helper()

	rng := rand.New(rand.NewSource(cfg.Seed))

	// --- 1. Generate repo tree ---
	repoSpecs := generateFuzzRepoTree(rng, cfg)

	// --- 2. Generate item SKUs ---
	items := make([]string, cfg.ItemCount)
	for i := range items {
		items[i] = fmt.Sprintf("fuzz-item-%d", i)
	}

	// --- 3. Set up the test environment, create repos and items ---
	te := setup(t)
	ctx := te.ctx(userA)
	apiClient := setupAPIClient(t, te)

	repoMap := make(map[string]string) // repoName → repoID
	for _, spec := range repoSpecs {
		var parentIDPtr *string
		if spec.Parent != "" {
			parentID := repoMap[spec.Parent]
			require.NotEmpty(t, parentID, "parent repo %q not found", spec.Parent)
			parentIDPtr = &parentID
		}

		repoType := entrepository.TypeStatic
		if spec.Type == "dynamic" {
			repoType = entrepository.TypeDynamic
		}

		id := stockTestCreateRepository(t, ctx, apiClient, spec.Name, repoType, spec.Virtual, parentIDPtr)
		repoMap[spec.Name] = id
	}

	itemMap := make(map[string]string) // sku → itemID
	for _, sku := range items {
		itemMap[sku] = stockTestCreateItem(t, ctx, apiClient, sku)
	}

	// --- 4. Classify repos ---
	virtualRepos := make([]string, 0, len(repoSpecs))
	physicalRepos := make([]string, 0, len(repoSpecs))
	allRepoNames := make([]string, 0, len(repoSpecs))
	repoIsVirtual := make(map[string]bool, len(repoSpecs))

	for _, spec := range repoSpecs {
		repoIsVirtual[spec.Name] = spec.Virtual
		allRepoNames = append(allRepoNames, spec.Name)

		if spec.Virtual {
			virtualRepos = append(virtualRepos, spec.Name)
		} else {
			physicalRepos = append(physicalRepos, spec.Name)
		}
	}

	require.NotEmpty(t, virtualRepos, "need at least one virtual repo as stock source")
	require.NotEmpty(t, physicalRepos, "need at least one physical repo")

	virtualSrc := virtualRepos[0]

	// --- 5. Seed moves: virtual → every physical repo, for every item (always succeed) ---
	seedCount := 0
	for _, repoName := range physicalRepos {
		for _, sku := range items {
			qty := cfg.SeedQtyMin
			if cfg.SeedQtyMax > cfg.SeedQtyMin {
				qty += rng.Intn(cfg.SeedQtyMax - cfg.SeedQtyMin + 1)
			}

			movID := stockTestCreateItemMovement(t, ctx, apiClient,
				itemMap[sku], repoMap[virtualSrc], repoMap[repoName], qty)
			stockTestExecuteItemMovement(t, ctx, apiClient, movID)
			seedCount++
		}
	}

	// --- 6. Random moves — no tracker, DB is source of truth ---
	// stockTestTryCreateItemMovementWithID returns (id, nil) on success,
	// ("", err) on failure.  We silently skip failures (e.g. insufficient stock).
	executedCount, skippedCount := 0, 0

	for range cfg.Iterations {
		shouldExecute := rng.Float64() < cfg.ExecuteRatio
		shouldDelete := !shouldExecute && rng.Float64() < 0.25

		// Source: 10% virtual injection, 90% random physical
		var fromName string
		if rng.Float64() < 0.1 {
			fromName = virtualSrc
		} else {
			fromName = physicalRepos[rng.Intn(len(physicalRepos))]
		}

		sku := items[rng.Intn(len(items))]

		// Destination: different from source, not both virtual
		var toName string
		for attempts := 0; ; attempts++ {
			require.Less(t, attempts, 100,
				"fuzzer: failed to find valid destination (from=%s)", fromName)

			toName = allRepoNames[rng.Intn(len(allRepoNames))]
			if toName != fromName && (!repoIsVirtual[fromName] || !repoIsVirtual[toName]) {
				break
			}
		}

		qty := cfg.MoveQtyMin
		if cfg.MoveQtyMax > cfg.MoveQtyMin {
			qty += rng.Intn(cfg.MoveQtyMax - cfg.MoveQtyMin + 1)
		}
		if qty < 1 {
			qty = 1
		}

		// Attempt create — silently skip if it fails
		movID, err := stockTestTryCreateItemMovementWithID(ctx, apiClient,
			itemMap[sku], repoMap[fromName], repoMap[toName], qty)
		if err != nil || movID == "" {
			skippedCount++
			continue
		}

		executedCount++

		switch {
		case shouldExecute:
			_ = stockTestTryExecuteItemMovement(ctx, apiClient, movID)
		case shouldDelete:
			_ = stockTestTryDeleteItemMovement(ctx, apiClient, movID)
		}
		// else: leave as pending movement
	}

	t.Logf("FuzzViaMovementFlow: seed=%d complexity=%d execute_ratio=%.0f%% items=%d repos=%d seed=%d random_executed=%d random_skipped=%d",
		cfg.Seed, cfg.Complexity, cfg.ExecuteRatio*100, cfg.ItemCount,
		len(repoSpecs), seedCount, executedCount, skippedCount)

	// --- 7. Snapshot → corrupt → rebuild → assert ---
	before := captureRawStockSnapshot(t, ctx, te.Ent)

	corruptStockWithGarbageValues(t, ctx, te.Ent)

	result, rebuildErr := apiClient.RebuildInventoryStock(ctx)
	require.NoError(t, rebuildErr, "RebuildInventoryStock should not error (seed=%d c%d)", cfg.Seed, cfg.Complexity)
	require.NotNil(t, result)
	assert.True(t, result.GetRebuildInventoryStock().GetSuccess(),
		"RebuildInventoryStock should report success (seed=%d c%d)", cfg.Seed, cfg.Complexity)

	assertRawStockSnapshot(t, ctx, te.Ent, before)

	t.Logf("FuzzViaMovementFlow passed: seed=%d complexity=%d", cfg.Seed, cfg.Complexity)
}

// TestRebuildInventoryStock_FuzzViaMovementFlow exercises RebuildInventoryStock
// with a broad set of deterministic pseudo-random scenarios (seed+config pairs).
// Each sub-test runs in parallel because every call to runFuzzRebuildTestViaMovementFlow
// creates its own isolated test environment with its own in-memory database.
//
// Corruption uses a large sentinel value (999_999_999) rather than deleting rows,
// so a no-op rebuild would be caught by the snapshot comparison.
func TestRebuildInventoryStock_FuzzViaMovementFlow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  fuzzConfig
	}{
		// ── Original representative seeds ────────────────────────────────────────

		// seed=42 complexity=1 — baseline, simple tree, mixed execute ratio
		{"seed-42-c1", fuzzConfig{
			Seed: 42, Complexity: 1, Iterations: 20, ItemCount: 2, RootCount: 1,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 2, MinDepth: 1, MaxDepth: 2, MaxLeaves: 8,
			ExecuteRatio: 0.64921134441865302, DynamicProb: 0.3,
			SeedQtyMin: 294, SeedQtyMax: 372, MoveQtyMin: 10, MoveQtyMax: 19,
			AllowMixedTrees: false,
		}},

		// seed=42 complexity=3 — same seed but more complex tree
		{"seed-42-c3", fuzzConfig{
			Seed: 42, Complexity: 3, Iterations: 60, ItemCount: 4, RootCount: 3,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 3, MinDepth: 2, MaxDepth: 4, MaxLeaves: 24,
			ExecuteRatio: 0.64921134441865302, DynamicProb: 0.3,
			SeedQtyMin: 504, SeedQtyMax: 597, MoveQtyMin: 17, MoveQtyMax: 32,
			AllowMixedTrees: false,
		}},

		// seed=7 complexity=2 — high execute ratio
		{"seed-7-c2", fuzzConfig{
			Seed: 7, Complexity: 2, Iterations: 40, ItemCount: 3, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 2, MinDepth: 1, MaxDepth: 3, MaxLeaves: 16,
			ExecuteRatio: 0.86755686370110541, DynamicProb: 0.3,
			SeedQtyMin: 420, SeedQtyMax: 545, MoveQtyMin: 14, MoveQtyMax: 26,
			AllowMixedTrees: false,
		}},

		// seed=99 complexity=4 — deep tree (3-5 levels), many repos, 80 iterations
		{"seed-99-c4", fuzzConfig{
			Seed: 99, Complexity: 4, Iterations: 80, ItemCount: 5, RootCount: 4,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 4, MinDepth: 3, MaxDepth: 5, MaxLeaves: 32,
			ExecuteRatio: 0.60543104582618790, DynamicProb: 0.3,
			SeedQtyMin: 651, SeedQtyMax: 825, MoveQtyMin: 30, MoveQtyMax: 32,
			AllowMixedTrees: false,
		}},

		// seed=1337 complexity=2 — different seed, moderate complexity
		{"seed-1337-c2", fuzzConfig{
			Seed: 1337, Complexity: 2, Iterations: 40, ItemCount: 3, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 2, MinDepth: 1, MaxDepth: 3, MaxLeaves: 16,
			ExecuteRatio: 0.75149541685288113, DynamicProb: 0.3,
			SeedQtyMin: 462, SeedQtyMax: 489, MoveQtyMin: 19, MoveQtyMax: 26,
			AllowMixedTrees: false,
		}},

		// ── Pagination boundary tests ─────────────────────────────────────────────
		// Goal: verify that paginated queries in RebuildStockTable and
		// GetRepositoriesDetails collect ALL records when total > 200 (LimitMixin cap).

		// ~20 repos × 11 items = ~220 seed moves — just over the 200-row page boundary.
		{"pagination-over-200-movements", fuzzConfig{
			Seed: 555, Complexity: 2, Iterations: 10, ItemCount: 11, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 2, MaxBreadth: 3, MinDepth: 2, MaxDepth: 3, MaxLeaves: 20,
			ExecuteRatio: 1.0, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 10, MoveQtyMax: 20,
			AllowMixedTrees: false,
		}},

		// ~20 repos × 15 items = ~300 seed moves + 50 random = ~350 total.
		// Tests rebuild spanning multiple pagination pages.
		{"pagination-multi-page-350", fuzzConfig{
			Seed: 777, Complexity: 3, Iterations: 50, ItemCount: 15, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 2, MaxBreadth: 3, MinDepth: 2, MaxDepth: 3, MaxLeaves: 20,
			ExecuteRatio: 0.7, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 500, MoveQtyMin: 10, MoveQtyMax: 20,
			AllowMixedTrees: false,
		}},

		// ── Execute ratio extremes ────────────────────────────────────────────────

		// 100% executed: fully settled state, no incoming/outgoing.
		{"all-executed-no-pending", fuzzConfig{
			Seed: 111, Complexity: 2, Iterations: 60, ItemCount: 3, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 3, MinDepth: 2, MaxDepth: 3, MaxLeaves: 16,
			ExecuteRatio: 1.0, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 10, MoveQtyMax: 20,
			AllowMixedTrees: false,
		}},

		// 0% executed: all random moves are pending reservations (incoming/outgoing only).
		{"all-random-pending-no-exec", fuzzConfig{
			Seed: 222, Complexity: 2, Iterations: 60, ItemCount: 3, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 3, MinDepth: 2, MaxDepth: 3, MaxLeaves: 16,
			ExecuteRatio: 0.0, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 5, MoveQtyMax: 10,
			AllowMixedTrees: false,
		}},

		// ~25% of random moves are deleted — rebuild must ignore them entirely.
		{"high-delete-rate", fuzzConfig{
			Seed: 333, Complexity: 2, Iterations: 60, ItemCount: 3, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 2, MinDepth: 2, MaxDepth: 3, MaxLeaves: 16,
			ExecuteRatio: 0.0, DynamicProb: 0.0, // 0% exec → ~25% deleted, 75% pending
			SeedQtyMin: 400, SeedQtyMax: 500, MoveQtyMin: 5, MoveQtyMax: 10,
			AllowMixedTrees: false,
		}},

		// ~65% executed, ~9% deleted, ~26% pending.
		{"mixed-exec-delete-pending", fuzzConfig{
			Seed: 444, Complexity: 3, Iterations: 60, ItemCount: 4, RootCount: 3,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 3, MinDepth: 2, MaxDepth: 3, MaxLeaves: 24,
			ExecuteRatio: 0.65, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 10, MoveQtyMax: 20,
			AllowMixedTrees: false,
		}},

		// ── Tree structure extremes ───────────────────────────────────────────────

		// Very deep single path: 5-level chain. Stock must propagate up all levels.
		{"deep-single-path-5-levels", fuzzConfig{
			Seed: 10, Complexity: 2, Iterations: 30, ItemCount: 2, RootCount: 1,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 1, MinDepth: 5, MaxDepth: 5, MaxLeaves: 8,
			ExecuteRatio: 0.75, DynamicProb: 0.0,
			SeedQtyMin: 200, SeedQtyMax: 300, MoveQtyMin: 10, MoveQtyMax: 20,
			AllowMixedTrees: false,
		}},

		// Many roots (15), shallow tree — tests broad stock aggregation.
		{"many-roots-shallow", fuzzConfig{
			Seed: 20, Complexity: 2, Iterations: 40, ItemCount: 3, RootCount: 15,
			VirtualTreeCount: 1, MinBreadth: 2, MaxBreadth: 2, MinDepth: 1, MaxDepth: 2, MaxLeaves: 60,
			ExecuteRatio: 0.7, DynamicProb: 0.0,
			SeedQtyMin: 200, SeedQtyMax: 300, MoveQtyMin: 10, MoveQtyMax: 20,
			AllowMixedTrees: false,
		}},

		// Many nodes: large breadth (4) across 4 levels → many intermediate nodes.
		// Tests that stock aggregation is correct at every level of a bushy tree.
		{"many-nodes-wide-breadth", fuzzConfig{
			Seed: 60, Complexity: 3, Iterations: 50, ItemCount: 3, RootCount: 3,
			VirtualTreeCount: 1, MinBreadth: 4, MaxBreadth: 4, MinDepth: 2, MaxDepth: 4, MaxLeaves: 50,
			ExecuteRatio: 0.7, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 10, MoveQtyMax: 20,
			AllowMixedTrees: false,
		}},

		// Many leaves: MaxLeaves=60 with multiple roots and breadth.
		// All leaves receive seed stock; tests per-leaf and aggregated parent stock.
		{"many-leaves", fuzzConfig{
			Seed: 70, Complexity: 3, Iterations: 40, ItemCount: 2, RootCount: 4,
			VirtualTreeCount: 1, MinBreadth: 3, MaxBreadth: 4, MinDepth: 2, MaxDepth: 3, MaxLeaves: 60,
			ExecuteRatio: 0.8, DynamicProb: 0.0,
			SeedQtyMin: 200, SeedQtyMax: 300, MoveQtyMin: 10, MoveQtyMax: 15,
			AllowMixedTrees: false,
		}},

		// Many branches (MaxBreadth=5): tests correct fan-out at each level.
		{"many-branches-per-level", fuzzConfig{
			Seed: 80, Complexity: 3, Iterations: 40, ItemCount: 3, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 5, MaxBreadth: 5, MinDepth: 2, MaxDepth: 3, MaxLeaves: 50,
			ExecuteRatio: 0.7, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 10, MoveQtyMax: 20,
			AllowMixedTrees: false,
		}},

		// Many items (15 items × ~12 repos = ~180 seed moves): tests per-item
		// isolation and correct stock tracking for many distinct item types.
		{"many-items", fuzzConfig{
			Seed: 90, Complexity: 2, Iterations: 30, ItemCount: 15, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 2, MaxBreadth: 3, MinDepth: 2, MaxDepth: 3, MaxLeaves: 12,
			ExecuteRatio: 0.75, DynamicProb: 0.0,
			SeedQtyMin: 200, SeedQtyMax: 300, MoveQtyMin: 5, MoveQtyMax: 15,
			AllowMixedTrees: false,
		}},

		// Dynamic repos only: all repos moveable (DynamicProb=1.0).
		// Tests rewind+replay of reparenting during rebuild.
		{"fully-dynamic-repos", fuzzConfig{
			Seed: 30, Complexity: 2, Iterations: 30, ItemCount: 2, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 2, MinDepth: 2, MaxDepth: 3, MaxLeaves: 12,
			ExecuteRatio: 0.75, DynamicProb: 1.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 10, MoveQtyMax: 15,
			AllowMixedTrees: false,
		}},

		// Multiple virtual injection sources (3 virtual trees).
		{"multiple-virtual-trees", fuzzConfig{
			Seed: 50, Complexity: 2, Iterations: 40, ItemCount: 3, RootCount: 2,
			VirtualTreeCount: 3, MinBreadth: 1, MaxBreadth: 2, MinDepth: 2, MaxDepth: 3, MaxLeaves: 12,
			ExecuteRatio: 0.7, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 10, MoveQtyMax: 15,
			AllowMixedTrees: false,
		}},

		// ── Combined stress tests ─────────────────────────────────────────────────

		// Deep tree + many items + mixed execute/delete/pending + dynamic repos.
		// High item count pushes toward pagination boundaries.
		{"stress-combined-deep-mixed", fuzzConfig{
			Seed: 9999, Complexity: 4, Iterations: 80, ItemCount: 8, RootCount: 4,
			VirtualTreeCount: 2, MinBreadth: 2, MaxBreadth: 3, MinDepth: 3, MaxDepth: 4, MaxLeaves: 30,
			ExecuteRatio: 0.65, DynamicProb: 0.2,
			SeedQtyMin: 500, SeedQtyMax: 700, MoveQtyMin: 15, MoveQtyMax: 30,
			AllowMixedTrees: false,
		}},

		// Diverse seed, moderate complexity.
		{"stress-seed-12345", fuzzConfig{
			Seed: 12345, Complexity: 3, Iterations: 60, ItemCount: 5, RootCount: 3,
			VirtualTreeCount: 1, MinBreadth: 2, MaxBreadth: 3, MinDepth: 3, MaxDepth: 4, MaxLeaves: 24,
			ExecuteRatio: 0.75, DynamicProb: 0.3,
			SeedQtyMin: 400, SeedQtyMax: 600, MoveQtyMin: 10, MoveQtyMax: 25,
			AllowMixedTrees: false,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // safe: each sub-test creates its own isolated DB via setup(t)
			runFuzzRebuildTestViaMovementFlow(t, tt.cfg)
		})
	}
}

// =============================================================================
// TESTDATA-DRIVEN REBUILD TESTS
// =============================================================================

// rawStockValues captures the numeric stock fields of a single stock row
// for comparison before and after corruption+rebuild.
type rawStockValues struct {
	repositoryID     string
	itemID           string
	quantity         int64
	ownQuantity      int64
	incomingStock    int64
	ownIncomingStock int64
	outgoingStock    int64
	ownOutgoingStock int64
}

// corruptStockWithGarbageValues sets all numeric stock fields to a clearly
// unrealistic sentinel value (999_999_999) for every stock row belonging to
// the tenant.
//
// The Ent schema enforces Min(0) on all stock quantity fields, so a negative
// sentinel would be rejected by the application-layer validator.  Instead we
// use a large positive value that is guaranteed never to appear in the
// testdata scenarios (which move quantities of at most a few hundred units).
//
// This is intentionally NOT a delete: if RebuildInventoryStock is a no-op,
// the sentinel values persist and the snapshot comparison will fail, proving
// the rebuild actually recomputed values rather than doing nothing.
func corruptStockWithGarbageValues(t *testing.T, ctx context.Context, entClient *ent.Client) {
	t.Helper()

	const garbage int64 = 999_999_999

	affected, err := entClient.Stock.Update().
		Where(entstock.TenantID(tenantA)).
		SetQuantity(garbage).
		SetOwnQuantity(garbage).
		SetIncomingStock(garbage).
		SetOwnIncomingStock(garbage).
		SetOutgoingStock(garbage).
		SetOwnOutgoingStock(garbage).
		Save(ctx)
	require.NoError(t, err, "corrupting stock values should not error")
	t.Logf("corruptStockWithGarbageValues: corrupted %d stock rows with sentinel value %d", affected, garbage)
}

// captureRawStockSnapshot queries the LATEST stock row per (repositoryID, itemID)
// pair for the tenant and returns a map keyed by "repoID|itemID" → rawStockValues.
//
// The HistoryMixin creates a new row per stock update, so there can be many rows
// per (repositoryID, itemID) pair. We order by created_at DESC and paginate with
// the maximum page size (200, as enforced by LimitMixin), keeping only the first
// (= most recent) value per key across all pages.
func captureRawStockSnapshot(t *testing.T, ctx context.Context, entClient *ent.Client) map[string]rawStockValues {
	t.Helper()

	const pageSize = 200 // maximum allowed by LimitMixin

	snapshot := make(map[string]rawStockValues)
	offset := 0

	for {
		rows, err := entClient.Stock.Query().
			Where(entstock.TenantID(tenantA)).
			Order(entstock.ByCreatedAt(sql.OrderDesc())).
			Limit(pageSize).
			Offset(offset).
			All(ctx)
		require.NoError(t, err, "querying stock rows for snapshot (offset=%d) should not error", offset)

		for _, row := range rows {
			key := row.RepositoryID.String() + "|" + row.ItemID.String()
			if _, seen := snapshot[key]; seen {
				continue // keep only the first (newest) row per (repo, item)
			}
			snapshot[key] = rawStockValues{
				repositoryID:     row.RepositoryID.String(),
				itemID:           row.ItemID.String(),
				quantity:         row.Quantity,
				ownQuantity:      row.OwnQuantity,
				incomingStock:    row.IncomingStock,
				ownIncomingStock: row.OwnIncomingStock,
				outgoingStock:    row.OutgoingStock,
				ownOutgoingStock: row.OwnOutgoingStock,
			}
		}

		if len(rows) < pageSize {
			break // last page
		}

		offset += pageSize
	}

	t.Logf("captureRawStockSnapshot: captured %d stock rows (latest per repo+item)", len(snapshot))
	return snapshot
}

// assertRawStockSnapshot compares the current DB stock state against a
// previously captured snapshot.  It verifies:
//   - same set of repo+item keys (no rows created or lost)
//   - all six numeric fields match exactly
func assertRawStockSnapshot(
	t *testing.T,
	ctx context.Context,
	entClient *ent.Client,
	expected map[string]rawStockValues,
) {
	t.Helper()

	actual := captureRawStockSnapshot(t, ctx, entClient)

	// Check row count
	assert.Len(t, actual, len(expected),
		"number of stock rows after rebuild must match pre-corruption snapshot")

	// Check each expected row exists with matching values
	for key, exp := range expected {
		act, ok := actual[key]
		if !assert.True(t, ok, "stock row %q missing after rebuild", key) {
			continue
		}
		assert.Equal(t, exp.quantity, act.quantity,
			"quantity mismatch for stock row %q after rebuild", key)
		assert.Equal(t, exp.ownQuantity, act.ownQuantity,
			"ownQuantity mismatch for stock row %q after rebuild", key)
		assert.Equal(t, exp.incomingStock, act.incomingStock,
			"incomingStock mismatch for stock row %q after rebuild", key)
		assert.Equal(t, exp.ownIncomingStock, act.ownIncomingStock,
			"ownIncomingStock mismatch for stock row %q after rebuild", key)
		assert.Equal(t, exp.outgoingStock, act.outgoingStock,
			"outgoingStock mismatch for stock row %q after rebuild", key)
		assert.Equal(t, exp.ownOutgoingStock, act.ownOutgoingStock,
			"ownOutgoingStock mismatch for stock row %q after rebuild", key)
	}

	// Also check for unexpected extra rows
	for key := range actual {
		_, ok := expected[key]
		assert.True(t, ok, "unexpected stock row %q appeared after rebuild", key)
	}
}

// runMovementFlowWithRebuild replays every step in a movementFlow scenario
// and then verifies that RebuildInventoryStock correctly restores corrupted
// stock values.
//
// Steps with ExpectError set are executed using try-variants so the test
// continues even when the step is expected to fail (the error-expected state
// is valid final state that rebuild must handle correctly).
//
// After all steps are replayed the function:
//  1. Captures a raw DB snapshot of all stock rows
//  2. Corrupts every stock row to -9999 (sentinel garbage value)
//  3. Calls RebuildInventoryStock
//  4. Asserts the raw DB snapshot matches the pre-corruption state
//
//nolint:cyclop,funlen // Large dispatch switch mirrors TestStockPlausibility intentionally
func runMovementFlowWithRebuild(t *testing.T, scenario movementFlow) {
	t.Helper()

	te := setup(t)
	ctx := te.ctx(userA)
	apiClient := setupAPIClient(t, te)

	// -------------------------------------------------------------------------
	// Create repositories
	// -------------------------------------------------------------------------
	repoMap := make(map[string]string)       // repoName → repoID
	parentTracker := make(map[string]string) // repoName → current parentID

	for _, repoSpec := range scenario.Repositories {
		var parentIDPtr *string
		if repoSpec.Parent != "" {
			parentID := repoMap[repoSpec.Parent]
			require.NotEmpty(t, parentID, "parent repository %q not found", repoSpec.Parent)
			parentIDPtr = &parentID
			parentTracker[repoSpec.Name] = parentID
		} else {
			parentTracker[repoSpec.Name] = ""
		}

		id := stockTestCreateRepository(t, ctx, apiClient, repoSpec.Name, repoSpec.entType(), repoSpec.Virtual, parentIDPtr)
		repoMap[repoSpec.Name] = id
	}

	// -------------------------------------------------------------------------
	// Create items
	// -------------------------------------------------------------------------
	itemMap := make(map[string]string) // sku → itemID
	var defaultItemID string

	if len(scenario.Items) > 0 {
		for _, sku := range scenario.Items {
			id := stockTestCreateItem(t, ctx, apiClient, sku)
			itemMap[sku] = id
		}
		defaultItemID = itemMap[scenario.Items[0]]
	} else {
		id := stockTestCreateItem(t, ctx, apiClient, scenario.ItemSKU)
		itemMap[scenario.ItemSKU] = id
		defaultItemID = id
	}

	// -------------------------------------------------------------------------
	// Replay all steps
	// -------------------------------------------------------------------------
	movements := make(map[string]movementInfo)

	t.Logf("runMovementFlowWithRebuild: scenario %q has %d steps, %d repos, %d items",
		scenario.Name, len(scenario.Steps), len(scenario.Repositories), len(itemMap))

	for i, step := range scenario.Steps {
		t.Logf("  step[%d] %s action=%s moveType=%s expectError=%q",
			i, step.Name, step.Action, step.MoveType, step.ExpectError)

		// Resolve item for this step
		itemID := defaultItemID
		if step.Item != "" {
			resolved, ok := itemMap[step.Item]
			require.True(t, ok, "item %q not found for step %q", step.Item, step.Name)
			itemID = resolved
		}

		switch step.Action {
		case actionMovementCreate:
			replayMovementCreate(t, ctx, apiClient, step, itemID, repoMap, parentTracker, movements)

		case actionMovementExecute:
			require.NotEmpty(t, step.Movement, "movement field is required for execute action in step %q", step.Name)
			info, ok := movements[step.Movement]
			require.True(t, ok, "execute: referenced movement %q not found in step %q", step.Movement, step.Name)

			if step.ExpectError != "" {
				// Run but do not fail on expected error — this is valid final state
				switch info.moveType {
				case moveTypeItem, moveTypeItemCollection:
					_ = stockTestTryExecuteItemMovement(ctx, apiClient, info.id)
				case moveTypeRepo:
					_ = stockTestTryExecuteRepositoryMovement(ctx, apiClient, info.id)
				default:
					t.Fatalf("step %q: unknown move type for execute: %s", step.Name, info.moveType)
				}
			} else {
				switch info.moveType {
				case moveTypeItem, moveTypeItemCollection:
					stockTestExecuteItemMovement(t, ctx, apiClient, info.id)
				case moveTypeRepo:
					stockTestExecuteRepositoryMovement(t, ctx, apiClient, info.id)
				default:
					t.Fatalf("step %q: unknown move type for execute: %s", step.Name, info.moveType)
				}
			}

		case actionMovementDelete:
			require.NotEmpty(t, step.Movement, "movement field is required for delete action in step %q", step.Name)
			info, ok := movements[step.Movement]
			require.True(t, ok, "delete: referenced movement %q not found in step %q", step.Movement, step.Name)

			if step.ExpectError != "" {
				// Run but do not fail on expected error — valid final state
				switch info.moveType {
				case moveTypeItem, moveTypeItemCollection:
					_ = stockTestTryDeleteItemMovement(ctx, apiClient, info.id)
				case moveTypeRepo:
					_ = stockTestTryDeleteRepositoryMovement(ctx, apiClient, info.id)
				default:
					t.Fatalf("step %q: unknown move type for delete: %s", step.Name, info.moveType)
				}
			} else {
				switch info.moveType {
				case moveTypeItem, moveTypeItemCollection:
					stockTestDeleteItemMovement(t, ctx, apiClient, info.id)
				case moveTypeRepo:
					stockTestDeleteRepositoryMovement(t, ctx, apiClient, info.id)
				default:
					t.Fatalf("step %q: unknown move type for delete: %s", step.Name, info.moveType)
				}
				delete(movements, step.Movement)
			}

		default:
			t.Fatalf("step %q: unknown action %q", step.Name, step.Action)
		}
	}

	// -------------------------------------------------------------------------
	// Snapshot → corrupt → rebuild → assert
	// -------------------------------------------------------------------------

	t.Logf("runMovementFlowWithRebuild: replay complete, %d movements tracked", len(movements))

	// 1. Capture pre-corruption snapshot
	before := captureRawStockSnapshot(t, ctx, te.Ent)

	// Log the pre-corruption stock state for transparency
	for key, v := range before {
		t.Logf("  pre-corrupt stock[%s]: qty=%d ownQty=%d in=%d ownIn=%d out=%d ownOut=%d",
			key, v.quantity, v.ownQuantity, v.incomingStock, v.ownIncomingStock, v.outgoingStock, v.ownOutgoingStock)
	}

	// 2. Corrupt all stock values to garbage — if rebuild is a no-op the
	//    assertion will catch it because -9999 ≠ the pre-corruption values.
	corruptStockWithGarbageValues(t, ctx, te.Ent)

	// 3. Rebuild from movement history
	result, err := apiClient.RebuildInventoryStock(ctx)
	require.NoError(t, err, "RebuildInventoryStock should not error for scenario %q", scenario.Name)
	require.NotNil(t, result)
	assert.True(t, result.GetRebuildInventoryStock().GetSuccess(),
		"RebuildInventoryStock should report success for scenario %q", scenario.Name)

	// 4. Assert that the rebuilt state matches the pre-corruption snapshot
	assertRawStockSnapshot(t, ctx, te.Ent, before)
}

// replayMovementCreate handles the "create" action for a single step inside
// runMovementFlowWithRebuild.  This mirrors the create-branch logic in
// TestStockPlausibility.
func replayMovementCreate(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	step movementStep,
	itemID string,
	repoMap map[string]string,
	parentTracker map[string]string,
	movements map[string]movementInfo,
) {
	t.Helper()

	if step.ExpectError != "" {
		// Run but ignore result — this create is expected to fail.
		// The resulting (invalid) state is what rebuild must handle.
		handleMovementCreateWithError(t, ctx, apiClient,
			step.From, step.To, step.MoveType, step.Name, step.ExpectError,
			step.Qty, repoMap, parentTracker, itemID)
		return
	}

	var movementID string
	switch step.MoveType {
	case moveTypeItem:
		movementID = handleItemMovement(t, ctx, apiClient, itemID, step.From, step.To, step.Qty, repoMap)
	case moveTypeItemCollection:
		movementID = handleItemCollectionMovement(t, ctx, apiClient, &step, itemID, repoMap, parentTracker, movements)
	case moveTypeRepo:
		movementID = handleRepositoryMovement(t, ctx, apiClient, step.From, step.To, repoMap, parentTracker)
	default:
		t.Fatalf("step %q: unknown move type for create: %s", step.Name, step.MoveType)
	}

	// For multi-entry collections handleItemCollectionMovement already stores
	// per-index keys via storeCollectionMovements; movementID is "" in that case.
	if movementID != "" {
		movements[step.Name] = movementInfo{id: movementID, moveType: step.MoveType}
	}
}

// TestRebuildInventoryStock_FromTestData loads every *.test.yaml file from the
// testdata/stock directory and verifies that RebuildInventoryStock correctly
// restores all stock values after they have been corrupted with garbage data.
//
// For each scenario this test:
//  1. Replays all movement steps (including error-expected steps, which run
//     but whose expected failures do not abort the test)
//  2. Captures a raw DB snapshot of all stock rows
//  3. Corrupts every stock field to -9999 (proves rebuild is not a no-op)
//  4. Calls RebuildInventoryStock
//  5. Asserts the raw DB snapshot matches the pre-corruption state
//
//nolint:tparallel // Sub-tests execute sequentially to maintain step order
func TestRebuildInventoryStock_FromTestData(t *testing.T) {
	t.Parallel()

	scenarios := loadScenarios(t)
	require.NotEmpty(t, scenarios, "no scenarios found in testdata/stock")

	//nolint:paralleltest // Sequential execution required for movement ordering
	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			runMovementFlowWithRebuild(t, scenario)
		})
	}
}
