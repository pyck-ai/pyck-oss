//nolint:testpackage // in-package test required: sortStockCreates is package-private.
package stock

import (
	"math/rand/v2"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
)

// sortKey is the tuple sortStockCreates orders by, read back out of a builder
// so the resulting sequence can be asserted on.
type sortKey struct {
	repo, item uuid.UUID
	version    int64
}

func keyOf(c *ent.StockCreate) sortKey {
	m := c.Mutation()
	repo, _ := m.RepositoryID()
	item, _ := m.ItemID()
	version, _ := m.Version()
	return sortKey{repo: repo, item: item, version: version}
}

func keysOf(creates []*ent.StockCreate) []sortKey {
	out := make([]sortKey, len(creates))
	for i, c := range creates {
		out[i] = keyOf(c)
	}
	return out
}

// stockFanOutFixture builds a batch shaped like the movement paths produce: a
// grid of repositories × items, plus one pair carrying two versions so the
// tiebreaker is exercised. Builders are constructed, never saved, so no driver
// is involved.
func stockFanOutFixture(t *testing.T) []*ent.StockCreate {
	t.Helper()
	client := ent.NewClient()

	repos := make([]uuid.UUID, 6)
	for i := range repos {
		repos[i] = uuid.New()
	}
	items := make([]uuid.UUID, 5)
	for i := range items {
		items[i] = uuid.New()
	}

	creates := make([]*ent.StockCreate, 0, len(repos)*len(items)+1)
	for _, repo := range repos {
		for _, item := range items {
			creates = append(creates, client.Stock.Create().
				SetRepositoryID(repo).SetItemID(item).SetVersion(1))
		}
	}
	creates = append(creates, client.Stock.Create().
		SetRepositoryID(repos[0]).SetItemID(items[0]).SetVersion(2))
	return creates
}

func shuffled(creates []*ent.StockCreate) []*ent.StockCreate {
	out := make([]*ent.StockCreate, len(creates))
	copy(out, creates)
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// TestSortStockCreates_SameBatchSortsIdentically is the property the fix rests
// on: two transactions fanning out over the same pairs must reach the same
// insert order, whatever order their map iterations produced.
func TestSortStockCreates_SameBatchSortsIdentically(t *testing.T) {
	t.Parallel()

	batch := stockFanOutFixture(t)

	first := shuffled(batch)
	second := shuffled(batch)
	require.NotEqual(t, keysOf(first), keysOf(second),
		"fixture too small to have been shuffled apart; the test would be vacuous")

	sortStockCreates(first)
	sortStockCreates(second)

	require.Equal(t, keysOf(first), keysOf(second),
		"two shuffles of one batch must sort to the same sequence")
}

// requireSortedFanOut asserts the batch is in the global insert order.
// Comparing through uuid.String() also pins the equivalence the byte
// comparator relies on: hex encoding preserves byte order.
func requireSortedFanOut(t *testing.T, creates []*ent.StockCreate) {
	t.Helper()
	keys := keysOf(creates)
	for i := 1; i < len(keys); i++ {
		prev, cur := keys[i-1], keys[i]
		switch {
		case prev.repo != cur.repo:
			require.Less(t, prev.repo.String(), cur.repo.String(),
				"repositories must ascend at index %d", i)
		case prev.item != cur.item:
			require.Less(t, prev.item.String(), cur.item.String(),
				"items must ascend within a repository at index %d", i)
		default:
			require.Less(t, prev.version, cur.version,
				"versions must ascend within a (repository, item) pair at index %d", i)
		}
	}
}

func TestSortStockCreates_OrdersByRepoThenItemThenVersion(t *testing.T) {
	t.Parallel()

	creates := shuffled(stockFanOutFixture(t))
	sortStockCreates(creates)

	requireSortedFanOut(t, creates)
}

// TestSortStockCreates_PreservesBatchContents guards that the fan-out still
// writes exactly the rows it built: the slice may be permuted, not changed.
func TestSortStockCreates_PreservesBatchContents(t *testing.T) {
	t.Parallel()

	creates := shuffled(stockFanOutFixture(t))

	before := make(map[*ent.StockCreate]int, len(creates))
	for _, c := range creates {
		before[c]++
	}

	sortStockCreates(creates)

	after := make(map[*ent.StockCreate]int, len(creates))
	for _, c := range creates {
		after[c]++
	}
	require.Equal(t, before, after, "sorting must permute the batch, not change it")
}

// TestSortStockCreates_ShortBatches covers the degenerate lengths callers hit —
// an empty repository fans out nothing at all.
func TestSortStockCreates_ShortBatches(t *testing.T) {
	t.Parallel()

	require.NotPanics(t, func() { sortStockCreates(nil) })
	require.NotPanics(t, func() { sortStockCreates([]*ent.StockCreate{}) })

	only := ent.NewClient().Stock.Create().
		SetRepositoryID(uuid.New()).SetItemID(uuid.New()).SetVersion(0)
	batch := []*ent.StockCreate{only}
	sortStockCreates(batch)
	require.Equal(t, []*ent.StockCreate{only}, batch)
}
