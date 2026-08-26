package resolvers_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"entgo.io/contrib/entgql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/resolvers"
)

// queryCurrentStocks invokes the currentStocks resolver directly (no first/last,
// so every head row is returned) and asserts it did not error. TotalCount is not
// populated when Paginate runs outside GraphQL field collection, so callers must
// assert on Edges, not TotalCount.
func queryCurrentStocks(te *testEnv, ctx context.Context, where *ent.StockWhereInput) *ent.StockConnection {
	te.t.Helper()
	return queryCurrentStocksOrdered(te, ctx, where, nil)
}

func queryCurrentStocksOrdered(te *testEnv, ctx context.Context, where *ent.StockWhereInput, orderBy *ent.StockOrder) *ent.StockConnection {
	te.t.Helper()
	r := resolvers.NewResolver("inventory", te.Ent, nil, te.StockService)
	conn, err := r.Query().CurrentStocks(ctx, nil, nil, nil, nil, orderBy, where)
	require.NoError(te.t, err)
	return conn
}

func currentStockByRepo(conn *ent.StockConnection) map[uuid.UUID]*ent.Stock {
	out := make(map[uuid.UUID]*ent.Stock, len(conn.Edges))
	for _, e := range conn.Edges {
		out[e.Node.RepositoryID] = e.Node
	}
	return out
}

// TestCurrentStocks pins the three currency invariants #1346 was filed against:
// version (not created_at) is the total order, dedup happens before value
// filtering, and reads are tenant-scoped and soft-delete aware.
func TestCurrentStocks(t *testing.T) {
	t.Parallel()

	t.Run("returns the highest-version row per repository in one call", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		repo1 := te.newRepository(ctx, userA).Create()
		repo2 := te.newRepository(ctx, userA).Create()

		// repo1 ledger: v0=100, v1=70 (current).
		te.newStock(ctx, userA, item.ID, repo1.ID).Quantity(100).Create()
		te.newStock(ctx, userA, item.ID, repo1.ID).Quantity(70).Create()
		// repo2 ledger: v0=5, v1=0, v2=42 (current). Note the interleaved zero row.
		te.newStock(ctx, userA, item.ID, repo2.ID).Quantity(5).Create()
		te.newStock(ctx, userA, item.ID, repo2.ID).Quantity(0).Create()
		te.newStock(ctx, userA, item.ID, repo2.ID).Quantity(42).Create()

		conn := queryCurrentStocks(te, ctx, &ent.StockWhereInput{
			ItemID:         &item.ID,
			RepositoryIDIn: []uuid.UUID{repo1.ID, repo2.ID},
		})

		require.Len(t, conn.Edges, 2)
		byRepo := currentStockByRepo(conn)
		require.Contains(t, byRepo, repo1.ID)
		require.Contains(t, byRepo, repo2.ID)
		assert.EqualValues(t, 70, byRepo[repo1.ID].Quantity)
		assert.EqualValues(t, 42, byRepo[repo2.ID].Quantity)
	})

	t.Run("newer zero row is not shadowed by a stale positive row", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		repo := te.newRepository(ctx, userA).Create()
		te.newStock(ctx, userA, item.ID, repo.ID).Quantity(10).Create() // v0 (stale positive)
		te.newStock(ctx, userA, item.ID, repo.ID).Quantity(0).Create()  // v1 (current zero)

		where := &ent.StockWhereInput{ItemID: &item.ID, RepositoryIDIn: []uuid.UUID{repo.ID}}

		// Unfiltered, the current row (qty 0) is the one returned.
		all := queryCurrentStocks(te, ctx, where)
		require.Len(t, all.Edges, 1)
		assert.EqualValues(t, 0, all.Edges[0].Node.Quantity)

		// quantityGT:0 is applied AFTER head-row selection: the head row (qty 0)
		// fails it and the stale v0 (qty 10) must not resurface. Result is empty.
		gtZero := int64(0)
		filtered := queryCurrentStocks(te, ctx, &ent.StockWhereInput{
			ItemID:         &item.ID,
			RepositoryIDIn: []uuid.UUID{repo.ID},
			QuantityGT:     &gtZero,
		})
		assert.Empty(t, filtered.Edges)
	})

	t.Run("results are scoped to the requesting tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		itemA := te.newItem(ctxA, userA).Create()
		repoA := te.newRepository(ctxA, userA).Create()
		te.newStock(ctxA, userA, itemA.ID, repoA.ID).Quantity(11).Create()

		itemB := te.newItem(ctxB, userB).Create()
		repoB := te.newRepository(ctxB, userB).Create()
		te.newStock(ctxB, userB, itemB.ID, repoB.ID).Quantity(22).Create()

		// Even naming tenant B's repository, tenant A only sees its own row.
		conn := queryCurrentStocks(te, ctxA, &ent.StockWhereInput{
			RepositoryIDIn: []uuid.UUID{repoA.ID, repoB.ID},
		})
		require.Len(t, conn.Edges, 1)
		assert.Equal(t, repoA.ID, conn.Edges[0].Node.RepositoryID)
		assert.EqualValues(t, 11, conn.Edges[0].Node.Quantity)
	})

	// Covers the nil-where branch (scope is then the request's tenants alone)
	// and the VERSION order field the type exposes.
	t.Run("without where, returns every head for the tenant, ordered by version", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		repoA := te.newRepository(ctx, userA).Create()
		repoB := te.newRepository(ctx, userA).Create()
		te.newStock(ctx, userA, item.ID, repoA.ID).Quantity(5).Create() // v0, head at repoA
		te.newStock(ctx, userA, item.ID, repoB.ID).Quantity(7).Create() // v0, superseded
		te.newStock(ctx, userA, item.ID, repoB.ID).Quantity(9).Create() // v1, head at repoB

		conn := queryCurrentStocksOrdered(te, ctx, nil, &ent.StockOrder{
			Direction: entgql.OrderDirectionDesc,
			Field:     ent.StockOrderFieldVersion,
		})

		require.Len(t, conn.Edges, 2, "one head per (repository, item), no where filter")
		assert.EqualValues(t, 1, conn.Edges[0].Node.Version)
		assert.EqualValues(t, 9, conn.Edges[0].Node.Quantity)
		assert.EqualValues(t, 0, conn.Edges[1].Node.Version)
		assert.EqualValues(t, 5, conn.Edges[1].Node.Quantity)
	})

	t.Run("a soft-deleted head row reports no current stock", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		repo := te.newRepository(ctx, userA).Create()
		te.newStock(ctx, userA, item.ID, repo.ID).Quantity(100).Create()         // v0 live
		te.newStock(ctx, userA, item.ID, repo.ID).Quantity(0).Deleted().Create() // v1 tombstone (current)

		conn := queryCurrentStocks(te, ctx, &ent.StockWhereInput{
			ItemID:         &item.ID,
			RepositoryIDIn: []uuid.UUID{repo.ID},
		})
		assert.Empty(t, conn.Edges)
	})
}

type currentStocksAsOfData struct {
	CurrentStocks struct {
		Edges []struct {
			Node struct {
				RepositoryID uuid.UUID
				Quantity     int64
				Version      int64
			}
		}
	}
}

// TestCurrentStocksAsOf exercises the `time` filter end-to-end (through the Time
// where-input resolver). A pair whose latest version was written after the cutoff
// must report its as-of-cutoff row, not vanish — head selection has to honour the
// created_at cutoff as scope. created_at is stamped by the history-mixin hook, so
// the cutoff is derived from v0's actual timestamp (the proven time-test pattern).
func TestCurrentStocksAsOf(t *testing.T) {
	t.Parallel()
	te := setup(t)
	ctx := te.ctx(userA)

	item := te.newItem(ctx, userA).Create()
	repo := te.newRepository(ctx, userA).Create()

	v0 := te.newStock(ctx, userA, item.ID, repo.ID).Quantity(10).Create()
	v1 := te.newStock(ctx, userA, item.ID, repo.ID).Quantity(99).Create()
	require.True(t, v1.CreatedAt.After(v0.CreatedAt), "v0 and v1 must have distinct DB created_at for a valid as-of cutoff")
	cutoff := v0.CreatedAt.Add(v1.CreatedAt.Sub(v0.CreatedAt) / 2)

	query := resolver.ParseTemplate(fmt.Sprintf(`
		query {
			currentStocks(where: {itemID: %q, repositoryIDIn: [%q], time: %q}) {
				edges { node { repositoryID quantity version } }
			}
		}`, item.ID, repo.ID, cutoff.Format(time.RFC3339Nano)))

	data := execOK[currentStocksAsOfData](te, ctx, query, nil)

	require.Len(t, data.CurrentStocks.Edges, 1, "the pair must report its as-of row, not vanish")
	assert.Equal(t, repo.ID, data.CurrentStocks.Edges[0].Node.RepositoryID)
	assert.EqualValues(t, 10, data.CurrentStocks.Edges[0].Node.Quantity, "as of the cutoff the current row is v0")
	assert.EqualValues(t, 0, data.CurrentStocks.Edges[0].Node.Version)
}
