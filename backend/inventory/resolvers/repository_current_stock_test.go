package resolvers_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
)

// repoCurrentStockData decodes a repositories connection that resolves the
// currentStock field on each node.
type repoCurrentStockData struct {
	Repositories struct {
		Edges []struct {
			Node struct {
				ID           uuid.UUID
				CurrentStock *struct {
					RepositoryID uuid.UUID
					Quantity     int64
					Version      int64
				}
			}
		}
	}
}

func repoCurrentStockQuery(itemID uuid.UUID, repoIDs ...uuid.UUID) resolver.Template {
	ids := make([]string, len(repoIDs))
	for i, id := range repoIDs {
		ids[i] = fmt.Sprintf("%q", id.String())
	}
	idList := "[" + join(ids) + "]"
	return resolver.ParseTemplate(fmt.Sprintf(`
		query {
			repositories(where: {idIn: %s}) {
				edges {
					node {
						id
						currentStock(itemId: %q) {
							repositoryID
							quantity
							version
						}
					}
				}
			}
		}`, idList, itemID.String()))
}

func join(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

func currentStockNodes(data repoCurrentStockData) map[uuid.UUID]*struct {
	RepositoryID uuid.UUID
	Quantity     int64
	Version      int64
} {
	out := make(map[uuid.UUID]*struct {
		RepositoryID uuid.UUID
		Quantity     int64
		Version      int64
	})
	for _, e := range data.Repositories.Edges {
		out[e.Node.ID] = e.Node.CurrentStock
	}
	return out
}

// TestRepositoryCurrentStock covers the second surface from #1346: the
// Repository.currentStock field.
func TestRepositoryCurrentStock(t *testing.T) {
	t.Parallel()

	t.Run("resolves the current row per repository, null when absent or deleted", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		withStock := te.newRepository(ctx, userA).Create()
		emptyRepo := te.newRepository(ctx, userA).Create()
		deletedHead := te.newRepository(ctx, userA).Create()

		// withStock: v0=100, v1=63 (current).
		te.newStock(ctx, userA, item.ID, withStock.ID).Quantity(100).Create()
		te.newStock(ctx, userA, item.ID, withStock.ID).Quantity(63).Create()
		// deletedHead: v0=100 live, v1 tombstone → no current stock.
		te.newStock(ctx, userA, item.ID, deletedHead.ID).Quantity(100).Create()
		te.newStock(ctx, userA, item.ID, deletedHead.ID).Quantity(0).Deleted().Create()
		// emptyRepo: never stocked.

		data := execOK[repoCurrentStockData](te, ctx,
			repoCurrentStockQuery(item.ID, withStock.ID, emptyRepo.ID, deletedHead.ID), nil)

		nodes := currentStockNodes(data)
		require.Len(t, nodes, 3)

		require.NotNil(t, nodes[withStock.ID], "repository with stock must resolve a current row")
		assert.Equal(t, withStock.ID, nodes[withStock.ID].RepositoryID)
		assert.EqualValues(t, 63, nodes[withStock.ID].Quantity)

		assert.Nil(t, nodes[emptyRepo.ID], "never-stocked repository must resolve null")
		assert.Nil(t, nodes[deletedHead.ID], "soft-deleted head must resolve null")
	})

	// The loader cannot collapse the fan-out: gqltx serializes every field
	// resolver behind a per-request mutex, so sibling resolutions never overlap
	// and no batch window can fill (measured: removing that middleware yields
	// one batch of N). Consumers needing one query for N repositories use
	// currentStocks(where:), asserted here so the cheaper path stays cheap.
	t.Run("field fan-out costs one query per repository; the bulk query costs one", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		const n = 5
		repoIDs := make([]uuid.UUID, n)
		for i := range repoIDs {
			repo := te.newRepository(ctx, userA).Create()
			repoIDs[i] = repo.ID
			te.newStock(ctx, userA, item.ID, repo.ID).Quantity(int64(10 + i)).Create() // v0
			te.newStock(ctx, userA, item.ID, repo.ID).Quantity(int64(20 + i)).Create() // v1 current
		}

		// Count Stock queries executed during the operation. Install the
		// interceptor only now, after seeding, so setup's own reads don't count.
		var stockQueries atomic.Int64
		te.Ent.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
			return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
				if _, ok := q.(*ent.StockQuery); ok {
					stockQueries.Add(1)
				}
				return next.Query(ctx, q)
			})
		}))

		data := execOK[repoCurrentStockData](te, ctx, repoCurrentStockQuery(item.ID, repoIDs...), nil)

		nodes := currentStockNodes(data)
		require.Len(t, nodes, n)
		for i, id := range repoIDs {
			require.NotNil(t, nodes[id])
			assert.EqualValues(t, 20+i, nodes[id].Quantity)
		}

		assert.EqualValues(t, n, stockQueries.Load(),
			"one head-selection query per repository is the serialized-resolver baseline; "+
				"fewer means batching started working (tighten this test), more means a new N+1")

		// Same data through the bulk surface: a single head-selection query.
		stockQueries.Store(0)
		conn := queryCurrentStocks(te, ctx, &ent.StockWhereInput{
			ItemID:         &item.ID,
			RepositoryIDIn: repoIDs,
		})
		require.Len(t, conn.Edges, n)
		assert.EqualValues(t, 1, stockQueries.Load(),
			"currentStocks must resolve every repository in one query")
	})
}
