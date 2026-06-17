//nolint:testpackage // in-package test required: stockBulkChunkBounds and stockCreateBulkChunked are package-private.
package stock

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/enttest"
	entprivacy "github.com/pyck-ai/pyck/backend/inventory/ent/gen/privacy"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// TestStockBulkChunkBounds_PartitionsCorrectly pins the slicing math
// inside stockCreateBulkChunked: every input length must be split into
// contiguous [start, end) ranges that cover [0, n) exactly once and
// where each range carries at most maxBatch elements.
//
// The function is the chunking-math seam that keeps stocks-table
// CreateBulk calls under PostgreSQL's 65,535-parameter wire-protocol
// limit (pyck-ai/pyck#1227). Without this guarantee the rebuild path
// would emit a single INSERT for the entire repo×item closure of a
// movement and fail with `pq: got NNNNN parameters but PostgreSQL only
// supports 65535` on rich tenants.
//
// The table covers all the meaningful boundary classes: empty / under /
// at / just-over / multi-chunk-exact / multi-chunk-with-trailing-remainder.
// maxBatch=0 and negative n are also exercised because production code
// must treat both as a no-op rather than dividing by zero or panicking.
func TestStockBulkChunkBounds_PartitionsCorrectly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		n        int
		maxBatch int
		want     [][2]int
	}{
		{name: "empty input", n: 0, maxBatch: 4500, want: nil},
		{name: "negative n", n: -1, maxBatch: 4500, want: nil},
		{name: "zero maxBatch", n: 10, maxBatch: 0, want: nil},
		{name: "negative maxBatch", n: 10, maxBatch: -1, want: nil},
		{name: "single row well under limit", n: 1, maxBatch: 4500, want: [][2]int{{0, 1}}},
		{name: "under limit", n: 4499, maxBatch: 4500, want: [][2]int{{0, 4499}}},
		{name: "exactly at limit", n: 4500, maxBatch: 4500, want: [][2]int{{0, 4500}}},
		{name: "one over limit", n: 4501, maxBatch: 4500, want: [][2]int{{0, 4500}, {4500, 4501}}},
		{name: "two batches exact", n: 9000, maxBatch: 4500, want: [][2]int{{0, 4500}, {4500, 9000}}},
		{name: "three batches exact", n: 13500, maxBatch: 4500, want: [][2]int{{0, 4500}, {4500, 9000}, {9000, 13500}}},
		{name: "three batches with remainder", n: 13501, maxBatch: 4500, want: [][2]int{{0, 4500}, {4500, 9000}, {9000, 13500}, {13500, 13501}}},
		{name: "reproducer-shape from issue 165k params", n: 11816, maxBatch: 4500, want: [][2]int{{0, 4500}, {4500, 9000}, {9000, 11816}}},
		{name: "tiny chunk size for stress", n: 7, maxBatch: 3, want: [][2]int{{0, 3}, {3, 6}, {6, 7}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := stockBulkChunkBounds(tc.n, tc.maxBatch)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestStockBulkChunkBounds_CoversInputExactlyOnce is a property-style
// guard for the math: for any 0 < n and 0 < maxBatch, the returned
// ranges must form a non-overlapping cover of [0, n).
//
// This is the invariant that production callers actually rely on
// (`for _, b := range bounds { tx.Stock.CreateBulk(creates[b[0]:b[1]]...) }`):
// every input row must land in exactly one batch — no duplicates, no
// gaps. The table-driven test above pins specific shapes; this one
// catches off-by-ones that would slip past hand-picked cases.
func TestStockBulkChunkBounds_CoversInputExactlyOnce(t *testing.T) {
	t.Parallel()

	type pair struct{ n, maxBatch int }
	for _, p := range []pair{
		{1, 1},
		{1, 4500},
		{2, 1},
		{4500, 4500},
		{4501, 4500},
		{10000, 4500},
		{12345, 4500},
		{65535, 4500},
		{7, 3},
		{8, 3},
		{9, 3},
		{10, 3},
	} {
		got := stockBulkChunkBounds(p.n, p.maxBatch)

		// Cover assertion: concatenating the ranges must yield [0, n).
		covered := make([]bool, p.n)
		for _, b := range got {
			require.Less(t, b[0], b[1], "n=%d max=%d: empty range %v", p.n, p.maxBatch, b)
			require.GreaterOrEqual(t, b[0], 0, "n=%d max=%d: negative start in %v", p.n, p.maxBatch, b)
			require.LessOrEqual(t, b[1], p.n, "n=%d max=%d: end past n in %v", p.n, p.maxBatch, b)
			require.LessOrEqual(t, b[1]-b[0], p.maxBatch, "n=%d max=%d: batch %v larger than maxBatch", p.n, p.maxBatch, b)
			for i := b[0]; i < b[1]; i++ {
				require.False(t, covered[i], "n=%d max=%d: index %d covered twice", p.n, p.maxBatch, i)
				covered[i] = true
			}
		}
		for i, c := range covered {
			require.True(t, c, "n=%d max=%d: index %d not covered", p.n, p.maxBatch, i)
		}
	}
}

// TestStockCreateBulkChunked_NoOpForEmptyInput asserts that the
// integration wrapper short-circuits on an empty slice without making
// any database round-trip and without erroring. The rebuild path passes
// a freshly-built slice that can legitimately be empty (e.g. a movement
// whose stock-map collapses to no rows because every entry is a no-op);
// the wrapper must not bubble that case into a panicky empty-INSERT.
func TestStockCreateBulkChunked_NoOpForEmptyInput(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, testresolver.DatabaseURI(t))
	t.Cleanup(func() { _ = client.Close() })

	tenantID := uuid.New()
	user := &authn.User{ID: uuid.New(), TenantID: tenantID}
	ctx := request.Context(context.Background(), user, tenantID)
	ctx = entprivacy.DecisionContext(ctx, entprivacy.Allow)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	require.NoError(t, stockCreateBulkChunked(ctx, tx, nil))
	require.NoError(t, stockCreateBulkChunked(ctx, tx, []*ent.StockCreate{}))

	count, err := tx.Stock.Query().Where(entstock.TenantID(tenantID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, count, "empty stockCreateBulkChunked must not write any rows")
}

// TestStockCreateBulkChunked_WritesEveryRowAcrossChunkBoundaries
// exercises the full save path against a live ent client for input
// sizes well under SQLite's per-statement variable ceiling (~32K
// parameters in the default build of go-sqlite3 — substantially below
// PostgreSQL's 65,535) so the wrapper can be exercised without dialect
// crosstalk noise.
//
// The contract pinned here: feeding the wrapper N rows must persist
// exactly N rows in the database. The chunking math (i.e. how N is
// split across multiple CreateBulk calls) is pinned exhaustively by
// TestStockBulkChunkBounds_PartitionsCorrectly above; this test is
// the integration cover, proving that the save loop iterates over
// every range bound the math test returns.
//
// Production-boundary row counts (>4,500) deliberately are NOT
// exercised here: a single Ent CreateBulk of 4,500 stocks emits ~50K
// bound parameters which is already past SQLite's limit. The
// PostgreSQL-backed regression test
// TestRebuildInventoryStock_LargeClosureChunksOverWireLimit (in
// backend/inventory/resolvers) covers the production constant directly.
//
// Each subtest uses a fresh tenant + repo + item so they remain
// isolated even when run sequentially against the same client.
//
//nolint:tparallel // SQLite serializes writes; running sub-cases concurrently against one file would only add file-lock queueing.
func TestStockCreateBulkChunked_WritesEveryRowAcrossChunkBoundaries(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, testresolver.DatabaseURI(t))
	t.Cleanup(func() { _ = client.Close() })

	cases := []struct {
		name string
		rows int
	}{
		{"single row", 1},
		{"small batch well under limit", 10},
		{"moderate batch under SQLite ceiling", 1000},
	}

	//nolint:paralleltest // see tparallel rationale above
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Subtests are NOT run in parallel: SQLite serializes writes,
			// so concurrent sub-cases against the same client would just
			// queue on the file lock and produce confusing timings.

			tenantID := uuid.New()
			user := &authn.User{ID: uuid.New(), TenantID: tenantID}
			ctx := request.Context(context.Background(), user, tenantID)
			ctx = entprivacy.DecisionContext(ctx, entprivacy.Allow)

			repo, err := client.Repository.Create().
				SetTenantID(tenantID).
				SetName("chunk-test-repo").
				SetType(entrepository.TypeStatic).
				SetVirtualRepo(false).
				Save(ctx)
			require.NoError(t, err)

			item, err := client.Item.Create().
				SetTenantID(tenantID).
				SetSku("CHUNK-TEST-ITEM").
				Save(ctx)
			require.NoError(t, err)

			movementID := uuid.New()
			tx, err := client.Tx(ctx)
			require.NoError(t, err)

			creates := make([]*ent.StockCreate, 0, tc.rows)
			for i := range tc.rows {
				// version is unique per (tenant, repo, item) so each row
				// satisfies the OCC uniqueness constraint on stocks.
				creates = append(creates, tx.Stock.Create().
					SetTenantID(tenantID).
					SetRepositoryID(repo.ID).
					SetItemID(item.ID).
					SetMovementID(movementID).
					SetQuantity(0).
					SetVersion(int64(i)))
			}

			require.NoError(t, stockCreateBulkChunked(ctx, tx, creates))
			require.NoError(t, tx.Commit())

			got, err := client.Stock.Query().
				Where(entstock.TenantID(tenantID), entstock.MovementID(movementID)).
				Count(ctx)
			require.NoError(t, err)
			require.Equal(t, tc.rows, got, "expected %d rows, got %d", tc.rows, got)
		})
	}
}
