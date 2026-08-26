package resolvers_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"

	"github.com/pyck-ai/pyck/backend/inventory/api"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
)

// TestStocksTimeFilterReturnsRequestedItemBeyondDefaultLimit is a regression
// test for the `stocks(where: { itemID, time })` "latest as of" filter returning
// 0 rows on tenants with more than the default query limit (mixin.Limit) of
// distinct (item, repository) pairs.
//
// Root cause: the `time` resolver pre-computed the "latest row per (item,
// repository)" ids in a separate, tenant-wide query that carried no explicit
// limit. The LimitMixin interceptor then silently capped that query at
// mixin.Limit rows with no ORDER BY, so on tenants with more pairs than the cap
// the requested item's rows fell outside the arbitrary capped set and the
// follow-up item filter matched nothing — returning 0 rows instead of the latest
// row per repository.
//
// The fix evaluates the latest-per-(item, repository) dedup inline (a correlated
// NOT EXISTS predicate) together with the query's own item filter and explicit
// pagination, so it is never silently truncated.
func TestStocksTimeFilterReturnsRequestedItemBeyondDefaultLimit(t *testing.T) {
	t.Parallel()

	env := setup(t)
	ctx := env.ctx(userA)
	apiClient := setupAPIClient(t, env)

	// Seed more than mixin.Limit distinct (item, repository) pairs for the tenant
	// so the tenant-wide latest-row pre-computation hits the default cap. A grid
	// of filler repositories x filler items keeps entity creation cheap.
	const fillerRepos, fillerItems = 21, 10
	require.Greater(t, fillerRepos*fillerItems, mixin.Limit,
		"test must seed more filler (item, repository) pairs than the default query limit to trigger the cap")

	repoIDs := make([]uuid.UUID, 0, fillerRepos)
	for r := range fillerRepos {
		id := stockTestCreateRepository(t, ctx, apiClient,
			fmt.Sprintf("filler-repo-%02d", r), entrepository.TypeStatic, false, nil)
		repoIDs = append(repoIDs, uuid.MustParse(id))
	}

	itemIDs := make([]uuid.UUID, 0, fillerItems)
	for i := range fillerItems {
		id := stockTestCreateItem(t, ctx, apiClient, fmt.Sprintf("FILLER-ITEM-%02d", i))
		itemIDs = append(itemIDs, uuid.MustParse(id))
	}

	for _, repoID := range repoIDs {
		for _, itemID := range itemIDs {
			env.newStock(ctx, userA, itemID, repoID).Quantity(1).Create()
		}
	}

	// The target item and its repositories are created AFTER every filler, so
	// their time-ordered v7 ids (and stock-row ids) sort after all filler pairs.
	// This guarantees the target falls outside the arbitrary capped set under the
	// buggy code, regardless of whether the planner scans by rowid or by index.
	targetItem := uuid.MustParse(stockTestCreateItem(t, ctx, apiClient, "TARGET-ITEM"))

	const targetRepoCount = 3
	targetRepos := make([]uuid.UUID, 0, targetRepoCount)
	for r := range targetRepoCount {
		id := stockTestCreateRepository(t, ctx, apiClient,
			fmt.Sprintf("target-repo-%02d", r), entrepository.TypeStatic, false, nil)
		targetRepos = append(targetRepos, uuid.MustParse(id))
	}
	for n, repoID := range targetRepos {
		env.newStock(ctx, userA, targetItem, repoID).Quantity(int64(10 + n)).Create()
	}

	// Query the latest stock per repository for the target item, as of a cutoff
	// after all writes. The correct result is exactly one row per target repo.
	cutoff := time.Now().Add(time.Hour)
	targetItemStr := targetItem.String()
	result, err := apiClient.GetStocks(ctx, api.GetStocksArgs{
		Where: &api.StockWhereInput{
			ItemID: &targetItemStr,
			Time:   &cutoff,
		},
		First: ptr(100),
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, targetRepoCount, result.GetStocks().GetTotalCount(),
		"time-filtered stocks must return the latest row per repository for the requested item, "+
			"even when the tenant has more than mixin.Limit (item, repository) pairs")
}
