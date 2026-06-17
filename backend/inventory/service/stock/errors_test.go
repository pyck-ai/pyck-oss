//nolint:testpackage // in-package test required: errOCCConflict and wrapOCCConflict are package-private.
package stock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// TestWrapOCCConflict_ConstraintNameMatch pins the contract documented in
// the OCC design notes: only a 23505 unique-violation whose ConstraintName
// equals the new per-version stocks index is translated to errOCCConflict.
// Other 23505s (e.g. an unrelated unique violation on items) and other
// SQLSTATEs pass through unchanged. This protects the retry path from
// silently swallowing unrelated unique conflicts.
func TestWrapOCCConflict_ConstraintNameMatch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		in           error
		wantSentinel bool
	}{
		{
			name:         "nil passthrough",
			in:           nil,
			wantSentinel: false,
		},
		{
			name: "matching 23505 on stock unique index returns sentinel",
			in: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: stockOCCUniqueIndex,
			},
			wantSentinel: true,
		},
		{
			name: "23505 on a different constraint passes through",
			in: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "item_tenant_id_sku_deleted_at",
			},
			wantSentinel: false,
		},
		{
			name: "non-23505 with the stock index name still passes through",
			in: &pgconn.PgError{
				Code:           "40001",
				ConstraintName: stockOCCUniqueIndex,
			},
			wantSentinel: false,
		},
		{
			name:         "non-pg error passes through",
			in:           errors.New("not a pg error"),
			wantSentinel: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := wrapOCCConflict(tc.in)
			if tc.in == nil {
				require.NoError(t, out)
				return
			}
			if tc.wantSentinel {
				require.ErrorIs(t, out, errOCCConflict)
				return
			}
			require.NotErrorIs(t, out, errOCCConflict,
				"input %v should have passed through unchanged", tc.in)
		})
	}
}

// TestWrapOCCConflict_UnwrapsThroughFmt confirms the helper still detects
// the matching pgconn.PgError when it has been wrapped via fmt.Errorf,
// which is how the service layer surfaces it ("failed inserting stocks: %w").
// errors.As walks the chain, so the helper must work transparently on a
// wrapped error.
func TestWrapOCCConflict_UnwrapsThroughFmt(t *testing.T) {
	t.Parallel()

	pgErr := &pgconn.PgError{Code: "23505", ConstraintName: stockOCCUniqueIndex}
	wrapped := fmt.Errorf("failed inserting stocks: %w", pgErr)

	require.ErrorIs(t, wrapOCCConflict(wrapped), errOCCConflict)
}

// TestWrapOCCConflict_LibPQError pins the production driver path. The pool is
// opened via otelsql.Register(dialect.Postgres) in common/db/postgresql.go,
// and the driver registered under "postgres" is lib/pq — so in production the
// 23505 unique-violation arrives as *pq.Error, not *pgconn.PgError. The helper
// must translate it to errOCCConflict exactly as it does for the pgx type;
// otherwise db.ErrIsRetryable never fires and the gqltx middleware never
// retries the losing transaction, surfacing the raw duplicate-key error to the
// caller. Covers both the bare error and the fmt.Errorf-wrapped form the
// service layer actually returns.
func TestWrapOCCConflict_LibPQError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		in           error
		wantSentinel bool
	}{
		{
			name:         "matching 23505 on stock unique index returns sentinel",
			in:           &pq.Error{Code: pgerrcode.UniqueViolation, Constraint: stockOCCUniqueIndex},
			wantSentinel: true,
		},
		{
			name:         "matching 23505 wrapped via fmt.Errorf returns sentinel",
			in:           fmt.Errorf("failed inserting stock: %w", &pq.Error{Code: pgerrcode.UniqueViolation, Constraint: stockOCCUniqueIndex}),
			wantSentinel: true,
		},
		{
			name:         "23505 on a different constraint passes through",
			in:           &pq.Error{Code: pgerrcode.UniqueViolation, Constraint: "item_tenant_id_sku_deleted_at"},
			wantSentinel: false,
		},
		{
			name:         "non-23505 with the stock index name passes through",
			in:           &pq.Error{Code: pgerrcode.SerializationFailure, Constraint: stockOCCUniqueIndex},
			wantSentinel: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := wrapOCCConflict(tc.in)
			if tc.wantSentinel {
				require.ErrorIs(t, out, errOCCConflict)
				return
			}
			require.NotErrorIs(t, out, errOCCConflict,
				"input %v should have passed through unchanged", tc.in)
		})
	}
}

// TestSourceRowOCCConflict_TwoGoroutinesRace pins the Phase 6.2 contract on
// real Postgres: two goroutines racing to insert the next-version stock
// row for the same (tenant, repository, item) group both target the same
// version slot; the unique index stock_tenant_id_repository_id_item_id_version
// admits exactly one, the other observes 23505, and wrapOCCConflict
// surfaces it as errOCCConflict.
//
// The test bypasses the ent layer and the full stocks/items/repositories
// schema by creating a dedicated table that mirrors the real index shape.
// The unique index on this table is named identically to the production
// index, so wrapOCCConflict's ConstraintName check exercises the exact
// production path. The full-schema retry test will run end-to-end through
// ent in Phase 6.3 — this one is the smallest possible thing that proves
// the race semantics.
//
// Skipped when PYCK_DATABASE_MASTER_URL is not set (e.g. in CI environments
// without the local sandbox Postgres). The local-setup task wires this
// var for development.
func TestSourceRowOCCConflict_TwoGoroutinesRace(t *testing.T) {
	t.Parallel()

	dsn := os.Getenv("PYCK_DATABASE_MASTER_URL")
	if dsn == "" {
		t.Skip("PYCK_DATABASE_MASTER_URL not set; skipping pg-backed OCC race test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	setupConn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = setupConn.Close(ctx) }()

	// Distinct table name per invocation so a leaked table from a
	// previous failed run does not block re-running. The unique index
	// keeps the production constraint name because that is exactly the
	// string wrapOCCConflict matches against — using a different name
	// here would defeat the purpose of the test.
	// Use the public schema explicitly: the production stocks table and
	// its unique index live in the inventory schema, so a same-named
	// index in public does not collide. The search_path on the local
	// sandbox role defaults to inventory, so without a fully qualified
	// table name CREATE INDEX would land in inventory and 42P07 against
	// the live production index.
	tableName := "public.stocks_occ_race_" + sanitizeUUIDForIdent(uuid.NewString())

	// Drop any leaked test tables from previous failed runs so the
	// CREATE INDEX below does not collide on the production constraint
	// name. The cleanup is scoped to public.stocks_occ_race_% to avoid
	// touching production tables in the inventory schema.
	if _, err := setupConn.Exec(ctx, `
		DO $$
		DECLARE r record;
		BEGIN
			FOR r IN
				SELECT t.relname AS tbl
				FROM pg_class t
				JOIN pg_namespace tn ON tn.oid = t.relnamespace
				WHERE tn.nspname = 'public'
				  AND t.relname LIKE 'stocks_occ_race_%'
			LOOP
				EXECUTE 'DROP TABLE IF EXISTS public.' || quote_ident(r.tbl) || ' CASCADE';
			END LOOP;
		END $$;`); err != nil {
		t.Fatalf("failed cleaning orphan test tables: %v", err)
	}

	createSQL := fmt.Sprintf(`CREATE TABLE %s (
		id uuid PRIMARY KEY,
		tenant_id uuid NOT NULL,
		repository_id uuid NOT NULL,
		item_id uuid NOT NULL,
		version bigint NOT NULL
	)`, tableName)
	_, err = setupConn.Exec(ctx, createSQL)
	require.NoError(t, err)
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dropCancel()
		_, _ = setupConn.Exec(dropCtx, "DROP TABLE IF EXISTS "+tableName+" CASCADE")
	})

	// The index lives in the same schema as the table (here: public),
	// so CREATE INDEX takes a bare index name. Postgres scopes index
	// names per-schema, so the production index in the inventory schema
	// does not collide with this one in public.
	indexSQL := fmt.Sprintf(`CREATE UNIQUE INDEX %s ON %s (tenant_id, repository_id, item_id, version)`,
		stockOCCUniqueIndex, tableName)
	_, err = setupConn.Exec(ctx, indexSQL)
	require.NoError(t, err)

	tenantID := uuid.New()
	repoID := uuid.New()
	itemID := uuid.New()
	const targetVersion = int64(0)

	results := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	ready := make(chan struct{}, 2)

	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			workerConn, connErr := pgx.Connect(ctx, dsn)
			if connErr != nil {
				results[idx] = connErr
				return
			}
			defer func() { _ = workerConn.Close(ctx) }()

			tx, txErr := workerConn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			if txErr != nil {
				results[idx] = txErr
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()

			ready <- struct{}{}
			<-start

			_, execErr := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s
				(id, tenant_id, repository_id, item_id, version)
				VALUES ($1, $2, $3, $4, $5)`, tableName),
				uuid.New(), tenantID, repoID, itemID, targetVersion)
			if execErr != nil {
				results[idx] = wrapOCCConflict(execErr)
				return
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				results[idx] = wrapOCCConflict(commitErr)
				return
			}
			results[idx] = nil
		}(i)
	}

	<-ready
	<-ready
	close(start)
	wg.Wait()

	successCount := 0
	conflictCount := 0
	for _, r := range results {
		switch {
		case r == nil:
			successCount++
		case errors.Is(r, errOCCConflict):
			conflictCount++
		default:
			t.Fatalf("unexpected error: %v", r)
		}
	}
	require.Equal(t, 1, successCount, "exactly one goroutine should commit successfully (got %d)", successCount)
	require.Equal(t, 1, conflictCount, "the loser should observe errOCCConflict (got %d)", conflictCount)
}

// sanitizeUUIDForIdent strips the dashes from a UUID string so it is a
// valid Postgres identifier suffix.
func sanitizeUUIDForIdent(s string) string {
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		}
	}
	return string(out)
}
