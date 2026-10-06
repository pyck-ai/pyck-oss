package resolvers_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/txid"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	enttransaction "github.com/pyck-ai/pyck/backend/inventory/ent/gen/transaction"
)

var (
	queryItemStocks = resolver.ParseTemplate(`query {
		inventoryItems(first: 10, orderBy: { direction: ASC, field: CREATED_AT }) {
			edges { node { id itemstocks { repositoryID quantity repository { id } } } }
		}
	}`)

	queryItemStockQuantities = resolver.ParseTemplate(`query {
		inventoryItems(first: 10) { edges { node { itemstocks { quantity } } } }
	}`)

	queryItemHistory = resolver.ParseTemplate(`query {
		inventoryItems(first: 10) {
			edges { node { itemmovementitems { id } itemtransactions { id } } }
		}
	}`)
)

type itemStocksData struct {
	InventoryItems struct {
		Edges []struct {
			Node struct {
				ID         uuid.UUID
				Itemstocks []struct {
					RepositoryID uuid.UUID
					Quantity     int64
					Repository   struct{ ID uuid.UUID }
				}
			}
		}
	}
}

type itemStockQuantitiesData struct {
	InventoryItems struct {
		Edges []struct {
			Node struct {
				Itemstocks []struct{ Quantity int64 }
			}
		}
	}
}

type itemHistoryData struct {
	InventoryItems struct {
		Edges []struct {
			Node struct {
				Itemmovementitems []struct{ ID uuid.UUID }
				Itemtransactions  []struct{ ID uuid.UUID }
			}
		}
	}
}

func TestItemStocks_ReturnsOnlyCurrentRows(t *testing.T) {
	t.Parallel()

	te := setup(t)
	ctx := te.ctx(userA)

	repoA := te.newRepository(ctx, userA).Name("repo-a").Create()
	repoB := te.newRepository(ctx, userA).Name("repo-b").Create()
	repoC := te.newRepository(ctx, userA).Name("repo-c").Create()

	first := te.newItem(ctx, userA).Sku("item-stocks-first").Create()
	for _, q := range []int64{1, 2, 3} {
		te.newStock(ctx, userA, first.ID, repoA.ID).Quantity(q).Create()
	}
	te.newStock(ctx, userA, first.ID, repoB.ID).Quantity(5).Create()
	te.newStock(ctx, userA, first.ID, repoB.ID).Quantity(7).Create()
	te.newStock(ctx, userA, first.ID, repoC.ID).Quantity(4).Create()
	te.newStock(ctx, userA, first.ID, repoC.ID).Quantity(0).Deleted().Create()

	second := te.newItem(ctx, userA).Sku("item-stocks-second").Create()
	te.newStock(ctx, userA, second.ID, repoA.ID).Quantity(10).Create()
	te.newStock(ctx, userA, second.ID, repoA.ID).Quantity(11).Create()

	data := execOK[itemStocksData](te, ctx, queryItemStocks, nil)

	got := map[uuid.UUID]map[uuid.UUID]int64{}
	for _, e := range data.InventoryItems.Edges {
		byRepo := map[uuid.UUID]int64{}
		for _, s := range e.Node.Itemstocks {
			require.Equal(t, s.RepositoryID, s.Repository.ID, "repository edge must resolve")
			_, dup := byRepo[s.RepositoryID]
			require.False(t, dup, "one row per repository expected")
			byRepo[s.RepositoryID] = s.Quantity
		}
		got[e.Node.ID] = byRepo
	}

	assert.Equal(t, map[uuid.UUID]int64{repoA.ID: 3, repoB.ID: 7}, got[first.ID],
		"only the highest version per repository; a deleted head hides its pair")
	assert.Equal(t, map[uuid.UUID]int64{repoA.ID: 11}, got[second.ID])
}

// More current rows than one query page, selecting no repository field: the
// paging must not depend on the client's selection.
func TestItemStocks_PagesPastQueryLimit(t *testing.T) {
	t.Parallel()

	te := setup(t)
	ctx := te.ctx(userA)

	item := te.newItem(ctx, userA).Sku("item-stocks-many").Create()
	repos := mixin.Limit + 5
	for i := range repos {
		repo := te.newRepository(ctx, userA).Name(fmt.Sprintf("many-%d", i)).Create()
		te.newStock(ctx, userA, item.ID, repo.ID).Quantity(1).Create()
		te.newStock(ctx, userA, item.ID, repo.ID).Quantity(2).Create()
	}

	data := execOK[itemStockQuantitiesData](te, ctx, queryItemStockQuantities, nil)

	require.Len(t, data.InventoryItems.Edges, 1)
	stocks := data.InventoryItems.Edges[0].Node.Itemstocks
	require.Len(t, stocks, repos)
	for _, s := range stocks {
		assert.Equal(t, int64(2), s.Quantity, "only current rows")
	}
}

func TestItemStocks_OmitsEmptyRows(t *testing.T) {
	t.Parallel()

	te := setup(t)
	ctx := te.ctx(userA)

	emptied := te.newRepository(ctx, userA).Name("emptied").Create()
	incoming := te.newRepository(ctx, userA).Name("incoming").Create()
	outgoing := te.newRepository(ctx, userA).Name("outgoing").Create()
	stocked := te.newRepository(ctx, userA).Name("stocked").Create()

	item := te.newItem(ctx, userA).Sku("item-stocks-empty").Create()
	te.newStock(ctx, userA, item.ID, emptied.ID).Quantity(5).Create()
	te.newStock(ctx, userA, item.ID, emptied.ID).Quantity(0).Create()
	te.newStock(ctx, userA, item.ID, incoming.ID).Quantity(0).Incoming(3).Create()
	te.newStock(ctx, userA, item.ID, outgoing.ID).Quantity(0).Outgoing(2).Create()
	te.newStock(ctx, userA, item.ID, stocked.ID).Quantity(4).Create()

	data := execOK[itemStocksData](te, ctx, queryItemStocks, nil)

	require.Len(t, data.InventoryItems.Edges, 1)
	stocks := data.InventoryItems.Edges[0].Node.Itemstocks
	repos := make([]uuid.UUID, 0, len(stocks))
	for _, s := range stocks {
		repos = append(repos, s.RepositoryID)
	}
	assert.ElementsMatch(t, []uuid.UUID{incoming.ID, outgoing.ID, stocked.ID}, repos,
		"an emptied repository is omitted; pending incoming or outgoing stock is not empty")
}

func TestItemStocks_RefusesAboveCap(t *testing.T) {
	t.Parallel()

	te := setup(t)
	ctx := te.ctx(userA)

	tmpl := te.newRepository(ctx, userA).Name("cap-template").Create()
	item := te.newItem(ctx, userA).Sku("item-stocks-cap").Create()

	execSQL(t, ctx, te.Ent, fmt.Sprintf(`INSERT INTO repositories
		SELECT (jsonb_populate_record(r, jsonb_build_object('id', gen_random_uuid(), 'name', 'cap-' || g))).*
		FROM repositories r, generate_series(1, 10001) g WHERE r.id = '%s'`, tmpl.ID))
	execSQL(t, ctx, te.Ent, fmt.Sprintf(`INSERT INTO stocks (id, tenant_id, created_at, created_by, quantity, item_id, repository_id, version)
		SELECT gen_random_uuid(), tenant_id, now(), '%s', 1, '%s', id, 0
		FROM repositories WHERE name LIKE 'cap-%%' AND id <> '%s'`, userA.ID, item.ID, tmpl.ID))

	execErr(te, ctx, queryItemStockQuantities, nil, "too many current stock rows")
}

func TestItemHistory_CapsAtLimitNewestFirst(t *testing.T) {
	t.Parallel()

	te := setup(t)
	ctx := te.ctx(userA)

	from := te.newRepository(ctx, userA).Name("from").Create()
	to := te.newRepository(ctx, userA).Name("to").Create()
	item := te.newItem(ctx, userA).Sku("item-history-cap").Create()

	oldestMovement := te.newItemMovement(ctx, userA, item.ID, from.ID, to.ID).Quantity(1).Create()
	for range mixin.Limit {
		te.newItemMovement(ctx, userA, item.ID, from.ID, to.ID).Quantity(1).Create()
	}

	newTransactions := func(n int) []*ent.Transaction {
		var out []*ent.Transaction
		require.NoError(t, te.withTx(ctx, func(tx *ent.Tx) error {
			builders := make([]*ent.TransactionCreate, n)
			for i := range builders {
				builders[i] = tx.Transaction.Create().
					SetTenantID(userA.TenantID).
					SetItemID(item.ID).
					SetRepositoryID(to.ID).
					SetQuantity(1).
					SetType(enttransaction.TypeInto)
			}
			var err error
			out, err = tx.Transaction.CreateBulk(builders...).Save(ent.NewTxContext(txid.With(ctx, txid.New()), tx))
			return err
		}))
		return out
	}
	oldestTransaction := newTransactions(1)[0]
	newTransactions(mixin.Limit)

	data := execOK[itemHistoryData](te, ctx, queryItemHistory, nil)

	require.Len(t, data.InventoryItems.Edges, 1)
	node := data.InventoryItems.Edges[0].Node
	require.Len(t, node.Itemmovementitems, mixin.Limit)
	require.Len(t, node.Itemtransactions, mixin.Limit)
	for _, m := range node.Itemmovementitems {
		assert.NotEqual(t, oldestMovement.ID, m.ID, "the oldest movement falls past the cap")
	}
	for _, tr := range node.Itemtransactions {
		assert.NotEqual(t, oldestTransaction.ID, tr.ID, "the oldest transaction falls past the cap")
	}
}
