package dataindex_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	_ "github.com/lib/pq"

	"github.com/pyck-ai/pyck/backend/common/dataindex"
)

// TestBackfillWalksPastPartialBatches pins the pass's stopping rule.
//
// The loop must end on what a batch selected, not on what it wrote: a batch
// whose rows were all locked elsewhere or already projected writes nothing while
// rows remain beyond it, and stopping there would leave the tail unindexed while
// boot reports success -- the silent miss the fail-boot guard exists to prevent.
// The race itself is not reproducible deterministically, so this pins the two
// properties that carry it: the cursor advances across several batches, and a
// second pass is a no-op.
func TestBackfillWalksPastPartialBatches(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("needs Docker")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine", tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() {
		if terr := container.Terminate(context.Background()); terr != nil {
			t.Logf("terminate container: %v", terr)
		}
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("close db: %v", cerr)
		}
	})

	table := "dataindex_cursor_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = db.ExecContext(ctx, `CREATE TABLE public.`+table+` (
		id uuid PRIMARY KEY, tenant_id uuid, data_type_slug text, data jsonb, data_ix_list1 jsonb)`)
	require.NoError(t, err)

	const rows = 5
	tenantID := uuid.New()
	for range rows {
		_, err = db.ExecContext(ctx,
			`INSERT INTO public.`+table+` (id, tenant_id, data_type_slug, data)
			 VALUES ($1, $2, 'slug', '{"l":["SN-1"]}'::jsonb)`,
			uuid.New(), tenantID)
		require.NoError(t, err)
	}

	bindings := dataindex.Bindings{
		"serials": {Name: "serials", Source: "/l", Slot: "data_ix_list1"},
	}

	// A batch size below the row count forces the cursor to carry across batches.
	const batchSize = 2
	n, err := dataindex.Backfill(ctx, db, "public", table, tenantID, "slug", bindings, batchSize)
	require.NoError(t, err)
	require.Equal(t, rows, n, "every row must be projected, not just the first batch")

	var unindexed int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.`+table+` WHERE data_ix_list1 IS NULL`).Scan(&unindexed))
	require.Zero(t, unindexed, "no row may be left behind the cursor")

	again, err := dataindex.Backfill(ctx, db, "public", table, tenantID, "slug", bindings, batchSize)
	require.NoError(t, err)
	require.Zero(t, again, "a second pass must match nothing")
}
