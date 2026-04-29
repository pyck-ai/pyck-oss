package resolvers_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type stockNode struct {
	ID               uuid.UUID
	TenantID         uuid.UUID
	RepositoryID     uuid.UUID
	ItemID           uuid.UUID
	Quantity         int64
	IncomingStock    int64
	OutgoingStock    int64
	OwnQuantity      int64
	OwnIncomingStock int64
	OwnOutgoingStock int64
	CreatedAt        string
	CreatedBy        uuid.UUID
}

type queryStocksData struct {
	Stocks struct {
		TotalCount int
		Edges      []struct {
			Node   stockNode
			Cursor string
		}
		PageInfo struct {
			HasNextPage     bool
			HasPreviousPage bool
			StartCursor     *string
			EndCursor       *string
		}
	}
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

func getStockLevelForTest(t *testing.T, te *testEnv, ctx context.Context, itemID, repoID uuid.UUID) queryStocksData {
	t.Helper()

	data := execOK[queryStocksData](te, ctx, stocksQueryTemplate, map[string]any{
		"Where": fmt.Sprintf(`{ itemID: %q, repositoryID: %q }`, itemID, repoID),
		"Last":  1,
	})

	return data
}

func runMovement(t *testing.T, te *testEnv, ctx context.Context, itemMovementID uuid.UUID) {
	t.Helper()
	execOK[executeItemMovementData](te, ctx, executeItemMovement, map[string]any{"ID": itemMovementID})
}

// =============================================================================
// STOCK QUERY TESTS
// =============================================================================

func TestStock_Query(t *testing.T) {
	t.Parallel()

	t.Run("sibling movement", func(t *testing.T) {
		t.Parallel()

		te := setup(t)
		defer te.Close(t)

		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Sku(testItem1.Sku).Create()

		incoming := te.newRepository(ctx, userA).Name("Incoming").Virtual(true).Create()
		repo1 := te.newRepository(ctx, userA).Name("Repo 1").Create()
		repo11 := te.newRepository(ctx, userA).Name("Repo 11").Parent(repo1.ID).Create()
		repo12 := te.newRepository(ctx, userA).Name("Repo 12").Parent(repo1.ID).Create()

		itemMovement := te.newItemMovement(ctx, userA, item.ID, incoming.ID, repo11.ID).
			Quantity(10).
			Create()

		runMovement(t, te, ctx, itemMovement.ID)

		stock := getStockLevelForTest(t, te, ctx, item.ID, repo1.ID)
		require.NotEmpty(t, stock.Stocks.Edges)
		assert.Equal(t, 1, stock.Stocks.TotalCount)
		assert.Equal(t, int64(10), stock.Stocks.Edges[0].Node.Quantity)
		assert.Equal(t, int64(0), stock.Stocks.Edges[0].Node.OwnQuantity)

		stock = getStockLevelForTest(t, te, ctx, item.ID, repo11.ID)
		require.NotEmpty(t, stock.Stocks.Edges)
		assert.Equal(t, 1, stock.Stocks.TotalCount)
		assert.Equal(t, int64(10), stock.Stocks.Edges[0].Node.Quantity)
		assert.Equal(t, int64(10), stock.Stocks.Edges[0].Node.OwnQuantity)

		itemMovement2 := te.newItemMovement(ctx, userA, item.ID, repo11.ID, repo12.ID).
			Quantity(1).
			Create()

		runMovement(t, te, ctx, itemMovement2.ID)

		stock = getStockLevelForTest(t, te, ctx, item.ID, repo1.ID)
		require.NotEmpty(t, stock.Stocks.Edges)
		assert.Equal(t, 2, stock.Stocks.TotalCount)
		assert.Equal(t, int64(10), stock.Stocks.Edges[0].Node.Quantity)
		assert.Equal(t, int64(0), stock.Stocks.Edges[0].Node.OwnQuantity)

		stock = getStockLevelForTest(t, te, ctx, item.ID, repo11.ID)
		require.NotEmpty(t, stock.Stocks.Edges)
		assert.Equal(t, 2, stock.Stocks.TotalCount)
		assert.Equal(t, int64(9), stock.Stocks.Edges[0].Node.Quantity)
		assert.Equal(t, int64(9), stock.Stocks.Edges[0].Node.OwnQuantity)

		stock = getStockLevelForTest(t, te, ctx, item.ID, repo12.ID)
		require.NotEmpty(t, stock.Stocks.Edges)
		assert.Equal(t, 1, stock.Stocks.TotalCount)
		assert.Equal(t, int64(1), stock.Stocks.Edges[0].Node.Quantity)
		assert.Equal(t, int64(1), stock.Stocks.Edges[0].Node.OwnQuantity)
	})

	t.Run("child to parent movement", func(t *testing.T) {
		t.Parallel()

		te := setup(t)
		defer te.Close(t)

		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Sku(testItem1.Sku).Create()

		incoming := te.newRepository(ctx, userA).Name("Incoming").Virtual(true).Create()
		repo1 := te.newRepository(ctx, userA).Name("Repo 1").Create()
		repo11 := te.newRepository(ctx, userA).Name("Repo 11").Parent(repo1.ID).Create()

		itemMovement := te.newItemMovement(ctx, userA, item.ID, incoming.ID, repo11.ID).
			Quantity(10).
			Create()

		runMovement(t, te, ctx, itemMovement.ID)

		stock := getStockLevelForTest(t, te, ctx, item.ID, repo1.ID)
		require.NotEmpty(t, stock.Stocks.Edges)
		assert.Equal(t, 1, stock.Stocks.TotalCount)
		assert.Equal(t, int64(10), stock.Stocks.Edges[0].Node.Quantity)
		assert.Equal(t, int64(0), stock.Stocks.Edges[0].Node.OwnQuantity)

		stock = getStockLevelForTest(t, te, ctx, item.ID, repo11.ID)
		require.NotEmpty(t, stock.Stocks.Edges)
		assert.Equal(t, 1, stock.Stocks.TotalCount)
		assert.Equal(t, int64(10), stock.Stocks.Edges[0].Node.Quantity)
		assert.Equal(t, int64(10), stock.Stocks.Edges[0].Node.OwnQuantity)

		itemMovement2 := te.newItemMovement(ctx, userA, item.ID, repo11.ID, repo1.ID).
			Quantity(1).
			Create()

		runMovement(t, te, ctx, itemMovement2.ID)

		stock = getStockLevelForTest(t, te, ctx, item.ID, repo1.ID)
		require.NotEmpty(t, stock.Stocks.Edges)
		assert.Equal(t, 2, stock.Stocks.TotalCount)
		assert.Equal(t, int64(10), stock.Stocks.Edges[0].Node.Quantity)
		assert.Equal(t, int64(1), stock.Stocks.Edges[0].Node.OwnQuantity)

		stock = getStockLevelForTest(t, te, ctx, item.ID, repo11.ID)
		require.NotEmpty(t, stock.Stocks.Edges)
		assert.Equal(t, 2, stock.Stocks.TotalCount)
		assert.Equal(t, int64(9), stock.Stocks.Edges[0].Node.Quantity)
		assert.Equal(t, int64(9), stock.Stocks.Edges[0].Node.OwnQuantity)
	})

	t.Run("foreign movement", func(t *testing.T) {
		t.Parallel()

		te := setup(t)
		defer te.Close(t)

		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Sku(testItem1.Sku).Create()

		incoming := te.newRepository(ctx, userA).Name("Incoming").Virtual(true).Create()
		repo1 := te.newRepository(ctx, userA).Name("Repo 1").Create()
		repo11 := te.newRepository(ctx, userA).Name("Repo 11").Parent(repo1.ID).Create()
		repo2 := te.newRepository(ctx, userA).Name("Repo 2").Create()
		repo21 := te.newRepository(ctx, userA).Name("Repo 21").Parent(repo2.ID).Create()

		itemMovement := te.newItemMovement(ctx, userA, item.ID, incoming.ID, repo11.ID).
			Quantity(10).
			Create()

		runMovement(t, te, ctx, itemMovement.ID)

		stock := getStockLevelForTest(t, te, ctx, item.ID, repo1.ID)
		require.NotEmpty(t, stock.Stocks.Edges)
		assert.Equal(t, 1, stock.Stocks.TotalCount)
		assert.Equal(t, int64(10), stock.Stocks.Edges[0].Node.Quantity)
		assert.Equal(t, int64(0), stock.Stocks.Edges[0].Node.OwnQuantity)

		stock = getStockLevelForTest(t, te, ctx, item.ID, repo11.ID)
		require.NotEmpty(t, stock.Stocks.Edges)
		assert.Equal(t, 1, stock.Stocks.TotalCount)
		assert.Equal(t, int64(10), stock.Stocks.Edges[0].Node.Quantity)
		assert.Equal(t, int64(10), stock.Stocks.Edges[0].Node.OwnQuantity)

		itemMovement2 := te.newItemMovement(ctx, userA, item.ID, repo11.ID, repo21.ID).
			Quantity(1).
			Create()

		runMovement(t, te, ctx, itemMovement2.ID)

		stock = getStockLevelForTest(t, te, ctx, item.ID, repo1.ID)
		require.NotEmpty(t, stock.Stocks.Edges)
		assert.Equal(t, 2, stock.Stocks.TotalCount)
		assert.Equal(t, int64(9), stock.Stocks.Edges[0].Node.Quantity)
		assert.Equal(t, int64(0), stock.Stocks.Edges[0].Node.OwnQuantity)

		stock = getStockLevelForTest(t, te, ctx, item.ID, repo11.ID)
		require.NotEmpty(t, stock.Stocks.Edges)
		assert.Equal(t, 2, stock.Stocks.TotalCount)
		assert.Equal(t, int64(9), stock.Stocks.Edges[0].Node.Quantity)
		assert.Equal(t, int64(9), stock.Stocks.Edges[0].Node.OwnQuantity)

		stock = getStockLevelForTest(t, te, ctx, item.ID, repo2.ID)
		require.NotEmpty(t, stock.Stocks.Edges)
		assert.Equal(t, 1, stock.Stocks.TotalCount)
		assert.Equal(t, int64(1), stock.Stocks.Edges[0].Node.Quantity)
		assert.Equal(t, int64(0), stock.Stocks.Edges[0].Node.OwnQuantity)

		stock = getStockLevelForTest(t, te, ctx, item.ID, repo21.ID)
		require.NotEmpty(t, stock.Stocks.Edges)
		assert.Equal(t, 1, stock.Stocks.TotalCount)
		assert.Equal(t, int64(1), stock.Stocks.Edges[0].Node.Quantity)
		assert.Equal(t, int64(1), stock.Stocks.Edges[0].Node.OwnQuantity)
	})
}

// =============================================================================
// GETSTOCKTREE QUERY TESTS
// =============================================================================

var getStockTreeTemplate = testresolver.ParseTemplate(`
	query {
		getStockTree(
			where: {{or .Where "null"}}
		) {
			edges {
				cursor
				node {
					repositoryID
					parentID
					stocks {
						id
						repositoryID
						itemID
						quantity
						ownQuantity
						incomingStock
						outgoingStock
					}
					children {
						cursor
						node {
							repositoryID
							stocks {
								id
								repositoryID
								itemID
								quantity
								ownQuantity
							}
						}
					}
				}
			}
			pageInfo {
				hasNextPage
				hasPreviousPage
			}
		}
	}`)

type stockTreeStockNode struct {
	ID            uuid.UUID
	RepositoryID  uuid.UUID
	ItemID        uuid.UUID
	Quantity      int64
	OwnQuantity   int64
	IncomingStock int64
	OutgoingStock int64
}

type stockTreeNode struct {
	RepositoryID uuid.UUID
	ParentID     *uuid.UUID
	Stocks       []stockTreeStockNode
	Children     []stockTreeEdge
}

type stockTreeEdge struct {
	Cursor string
	Node   stockTreeNode
}

type queryStockTreeData struct {
	GetStockTree struct {
		Edges    []stockTreeEdge
		PageInfo struct {
			HasNextPage     bool
			HasPreviousPage bool
		}
	}
}

// findStockTreeRepo recursively searches the tree edges for a repository by ID.
func findStockTreeRepo(edges []stockTreeEdge, repoID uuid.UUID) *stockTreeNode {
	for i := range edges {
		if edges[i].Node.RepositoryID == repoID {
			return &edges[i].Node
		}
		if found := findStockTreeRepo(edges[i].Node.Children, repoID); found != nil {
			return found
		}
	}
	return nil
}

func TestGetStockTree(t *testing.T) {
	t.Parallel()

	t.Run("returns latest stock per repository-item pair", func(t *testing.T) {
		t.Parallel()

		te := setup(t)
		defer te.Close(t)

		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Sku("stocktree-item-1").Create()
		incoming := te.newRepository(ctx, userA).Name("Incoming").Virtual(true).Create()
		warehouse := te.newRepository(ctx, userA).Name("Warehouse").Create()
		shelf := te.newRepository(ctx, userA).Name("Shelf").Parent(warehouse.ID).Create()

		// Move 10 items from virtual → shelf, execute.
		mv1 := te.newItemMovement(ctx, userA, item.ID, incoming.ID, shelf.ID).Quantity(10).Create()
		runMovement(t, te, ctx, mv1.ID)

		// Move 3 items from shelf → warehouse, execute.
		// This creates a second stock record per repo.
		mv2 := te.newItemMovement(ctx, userA, item.ID, shelf.ID, warehouse.ID).Quantity(3).Create()
		runMovement(t, te, ctx, mv2.ID)

		// Query the stock tree — should return only the latest stock per (repo, item).
		data := execOK[queryStockTreeData](te, ctx, getStockTreeTemplate, map[string]any{})

		// Find warehouse in tree.
		whNode := findStockTreeRepo(data.GetStockTree.Edges, warehouse.ID)
		require.NotNil(t, whNode, "warehouse should be in the tree")

		// Warehouse: aggregated quantity should be 10 (7 in shelf + 3 own).
		require.Len(t, whNode.Stocks, 1, "warehouse should have exactly one stock entry (latest)")
		assert.Equal(t, int64(10), whNode.Stocks[0].Quantity)
		assert.Equal(t, int64(3), whNode.Stocks[0].OwnQuantity)

		// Find shelf in tree.
		shNode := findStockTreeRepo(data.GetStockTree.Edges, shelf.ID)
		require.NotNil(t, shNode, "shelf should be in the tree")
		require.Len(t, shNode.Stocks, 1, "shelf should have exactly one stock entry (latest)")
		assert.Equal(t, int64(7), shNode.Stocks[0].Quantity)
		assert.Equal(t, int64(7), shNode.Stocks[0].OwnQuantity)
	})

	t.Run("multiple items in same repository", func(t *testing.T) {
		t.Parallel()

		te := setup(t)
		defer te.Close(t)

		ctx := te.ctx(userA)

		item1 := te.newItem(ctx, userA).Sku("stocktree-multi-1").Create()
		item2 := te.newItem(ctx, userA).Sku("stocktree-multi-2").Create()
		incoming := te.newRepository(ctx, userA).Name("Incoming").Virtual(true).Create()
		repo := te.newRepository(ctx, userA).Name("Repo").Create()

		// Move 10 of item1 and 5 of item2 into repo.
		mv1 := te.newItemMovement(ctx, userA, item1.ID, incoming.ID, repo.ID).Quantity(10).Create()
		runMovement(t, te, ctx, mv1.ID)
		mv2 := te.newItemMovement(ctx, userA, item2.ID, incoming.ID, repo.ID).Quantity(5).Create()
		runMovement(t, te, ctx, mv2.ID)

		data := execOK[queryStockTreeData](te, ctx, getStockTreeTemplate, map[string]any{})

		repoNode := findStockTreeRepo(data.GetStockTree.Edges, repo.ID)
		require.NotNil(t, repoNode)
		require.Len(t, repoNode.Stocks, 2, "repo should have stock entries for both items")

		stockByItem := make(map[uuid.UUID]stockTreeStockNode)
		for _, s := range repoNode.Stocks {
			stockByItem[s.ItemID] = s
		}

		assert.Equal(t, int64(10), stockByItem[item1.ID].Quantity)
		assert.Equal(t, int64(5), stockByItem[item2.ID].Quantity)
	})

	t.Run("point in time query", func(t *testing.T) {
		t.Parallel()

		te := setup(t)
		defer te.Close(t)

		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Sku("stocktree-time-1").Create()
		incoming := te.newRepository(ctx, userA).Name("Incoming").Virtual(true).Create()
		repo := te.newRepository(ctx, userA).Name("Repo").Create()

		// Move 10 items, execute.
		mv1 := te.newItemMovement(ctx, userA, item.ID, incoming.ID, repo.ID).Quantity(10).Create()
		runMovement(t, te, ctx, mv1.ID)

		// Ensure the second movement gets a strictly later created_at / UUIDv7.
		time.Sleep(50 * time.Millisecond)

		// Record a time between the two movements.
		midTime := time.Now().UTC()

		time.Sleep(50 * time.Millisecond)

		// Move 5 more items, execute.
		mv2 := te.newItemMovement(ctx, userA, item.ID, incoming.ID, repo.ID).Quantity(5).Create()
		runMovement(t, te, ctx, mv2.ID)

		// Query with time filter — should see stock as of midTime (quantity=10, not 15).
		data := execOK[queryStockTreeData](te, ctx, getStockTreeTemplate, map[string]any{
			"Where": fmt.Sprintf(`{ time: %q }`, midTime.Format(time.RFC3339Nano)),
		})

		repoNode := findStockTreeRepo(data.GetStockTree.Edges, repo.ID)
		require.NotNil(t, repoNode, "repo should be in the tree")
		require.Len(t, repoNode.Stocks, 1)
		assert.Equal(t, int64(10), repoNode.Stocks[0].Quantity,
			"point-in-time query should return stock as of the cutoff time")
	})
}

// =============================================================================
// STOCKS QUERY WITH TIME FILTER (Time resolver)
// =============================================================================

var stocksWithTimeTemplate = testresolver.ParseTemplate(`
	query {
		stocks(
			where: {{or .Where "null"}},
			last: {{or .Last "null"}},
		) {
			totalCount
			edges {
				node {
					id
					repositoryID
					itemID
					quantity
					ownQuantity
				}
			}
		}
	}`)

type queryStocksWithTimeData struct {
	Stocks struct {
		TotalCount int
		Edges      []struct {
			Node struct {
				ID           uuid.UUID
				RepositoryID uuid.UUID
				ItemID       uuid.UUID
				Quantity     int64
				OwnQuantity  int64
			}
		}
	}
}

func TestStocks_TimeFilter(t *testing.T) {
	t.Parallel()

	t.Run("filters stocks by point in time", func(t *testing.T) {
		t.Parallel()

		te := setup(t)
		defer te.Close(t)

		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Sku("time-filter-1").Create()
		incoming := te.newRepository(ctx, userA).Name("Incoming").Virtual(true).Create()
		repo := te.newRepository(ctx, userA).Name("Repo").Create()

		// Move 10, execute.
		mv1 := te.newItemMovement(ctx, userA, item.ID, incoming.ID, repo.ID).Quantity(10).Create()
		runMovement(t, te, ctx, mv1.ID)

		// Ensure the second movement gets a strictly later created_at / UUIDv7.
		time.Sleep(50 * time.Millisecond)
		midTime := time.Now().UTC()
		time.Sleep(50 * time.Millisecond)

		// Move 5 more, execute.
		mv2 := te.newItemMovement(ctx, userA, item.ID, incoming.ID, repo.ID).Quantity(5).Create()
		runMovement(t, te, ctx, mv2.ID)

		// Query with time filter — should only see stocks created before midTime.
		data := execOK[queryStocksWithTimeData](te, ctx, stocksWithTimeTemplate, map[string]any{
			"Where": fmt.Sprintf(`{ repositoryID: %q, itemID: %q, time: %q }`,
				repo.ID, item.ID, midTime.Format(time.RFC3339Nano)),
			"Last": 1,
		})

		require.NotEmpty(t, data.Stocks.Edges)
		// The latest stock before midTime should have quantity=10 (not 15).
		assert.Equal(t, int64(10), data.Stocks.Edges[0].Node.Quantity,
			"time-filtered query should return stock as of the cutoff time")
	})
}

// =============================================================================
// NET STOCK FILTER TESTS
// =============================================================================

// TestStocks_NetStockGteFilter tests the netStockGTE filter on the stocks query.
// Net stock is calculated as: own_quantity + own_incoming_stock - own_outgoing_stock.
//
//nolint:tparallel // Subtests must run sequentially because they share state.
func TestStocks_NetStockGteFilter(t *testing.T) {
	t.Parallel()

	te := setup(t)
	defer te.Close(t)

	ctx := te.ctx(userA)

	// Create item and repositories
	item := te.newItem(ctx, userA).Sku("netstock-filter-item").Create()
	virtual := te.newRepository(ctx, userA).Name("Virtual").Virtual(true).Create()
	warehouse := te.newRepository(ctx, userA).Name("Warehouse").Create()

	// Create three child repositories with different stock levels
	repoA := te.newRepository(ctx, userA).Name("Repo-A").Parent(warehouse.ID).Create()
	repoB := te.newRepository(ctx, userA).Name("Repo-B").Parent(warehouse.ID).Create()
	repoC := te.newRepository(ctx, userA).Name("Repo-C").Parent(warehouse.ID).Create()

	// Setup stock scenarios:
	// Repo-A: quantity=50, net=50 (50+0-0)
	mvA := te.newItemMovement(ctx, userA, item.ID, virtual.ID, repoA.ID).Quantity(50).Create()
	runMovement(t, te, ctx, mvA.ID)

	// Repo-B: quantity=30, incoming=10, net=40 (30+10-0)
	mvB1 := te.newItemMovement(ctx, userA, item.ID, virtual.ID, repoB.ID).Quantity(30).Create()
	runMovement(t, te, ctx, mvB1.ID)
	_ = te.newItemMovement(ctx, userA, item.ID, virtual.ID, repoB.ID).Quantity(10).Create() // pending incoming

	// Repo-C: quantity=40, outgoing=25, net=15 (40+0-25)
	mvC1 := te.newItemMovement(ctx, userA, item.ID, virtual.ID, repoC.ID).Quantity(40).Create()
	runMovement(t, te, ctx, mvC1.ID)
	_ = te.newItemMovement(ctx, userA, item.ID, repoC.ID, virtual.ID).Quantity(25).Create() // pending outgoing

	// Query template with netStockGTE filter
	netStockQueryTemplate := testresolver.ParseTemplate(`
		query {
			stocks(
				where: { itemID: {{.ItemID}}, netStockGTE: {{.NetStockGTE}} },
			) {
				totalCount
				edges {
					node {
						id
						repositoryID
						quantity
						incomingStock
						outgoingStock
						ownQuantity
						ownIncomingStock
						ownOutgoingStock
					}
				}
			}
		}
	`)

	// Test 1: Filter for net stock >= 50 (should return only Repo-A)
	//nolint:paralleltest
	t.Run("netStockGTE 50 returns only Repo-A", func(t *testing.T) {
		data := execOK[queryStocksData](te, ctx, netStockQueryTemplate, map[string]any{
			"ItemID":      fmt.Sprintf("%q", item.ID),
			"NetStockGTE": 50,
		})

		require.Equal(t, 1, data.Stocks.TotalCount, "should return 1 stock record")
		require.Len(t, data.Stocks.Edges, 1)
		assert.Equal(t, repoA.ID, data.Stocks.Edges[0].Node.RepositoryID)
		assert.Equal(t, int64(50), data.Stocks.Edges[0].Node.OwnQuantity)
	})

	// Test 2: Filter for net stock >= 40 (should return Repo-A and Repo-C)
	// Note: Repo-B has ownQuantity=30 (pending incoming not counted), Repo-C has ownQuantity=40
	//nolint:paralleltest
	t.Run("netStockGTE 40 returns Repo-A and Repo-C", func(t *testing.T) {
		data := execOK[queryStocksData](te, ctx, netStockQueryTemplate, map[string]any{
			"ItemID":      fmt.Sprintf("%q", item.ID),
			"NetStockGTE": 40,
		})

		require.Equal(t, 2, data.Stocks.TotalCount, "should return 2 stock records")
		require.Len(t, data.Stocks.Edges, 2)

		// Verify both repos are returned (order may vary)
		returnedRepos := []uuid.UUID{
			data.Stocks.Edges[0].Node.RepositoryID,
			data.Stocks.Edges[1].Node.RepositoryID,
		}
		assert.Contains(t, returnedRepos, repoA.ID)
		assert.Contains(t, returnedRepos, repoC.ID)
	})

	// Test 3: Filter for net stock >= 15 (should return all three repos)
	//nolint:paralleltest
	t.Run("netStockGTE 15 returns all repos", func(t *testing.T) {
		data := execOK[queryStocksData](te, ctx, netStockQueryTemplate, map[string]any{
			"ItemID":      fmt.Sprintf("%q", item.ID),
			"NetStockGTE": 15,
		})

		require.Equal(t, 3, data.Stocks.TotalCount, "should return 3 stock records")
		require.Len(t, data.Stocks.Edges, 3)

		// Verify all three repos are returned
		returnedRepos := []uuid.UUID{
			data.Stocks.Edges[0].Node.RepositoryID,
			data.Stocks.Edges[1].Node.RepositoryID,
			data.Stocks.Edges[2].Node.RepositoryID,
		}
		assert.Contains(t, returnedRepos, repoA.ID)
		assert.Contains(t, returnedRepos, repoB.ID)
		assert.Contains(t, returnedRepos, repoC.ID)
	})

	// Test 4: Filter for net stock >= 100 (should return nothing)
	//nolint:paralleltest
	t.Run("netStockGTE 100 returns no results", func(t *testing.T) {
		data := execOK[queryStocksData](te, ctx, netStockQueryTemplate, map[string]any{
			"ItemID":      fmt.Sprintf("%q", item.ID),
			"NetStockGTE": 100,
		})

		assert.Equal(t, 0, data.Stocks.TotalCount, "should return 0 stock records")
		assert.Empty(t, data.Stocks.Edges)
	})
}
