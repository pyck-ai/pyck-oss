//nolint:testpackage // in-package test: errOCCConflict / wrapOCCConflict / stockOCCUniqueIndex are package-private.
package stock

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// TestPG_TwoGoroutineVersionRace is an unconditional Postgres-backed port of
// TestSourceRowOCCConflict_TwoGoroutinesRace. The original skips unless
// PYCK_DATABASE_MASTER_URL is set; this variant uses the testcontainer started
// by TestMain and always runs as part of the TestPG_ suite.
//
// Two goroutines race to insert the same (tenant, repository, item, version) into
// a mirror table that carries the production unique index name. Exactly one must
// commit; the other must observe errOCCConflict.
func TestPG_TwoGoroutineVersionRace(t *testing.T) {
	t.Parallel()
	requirePG(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use the bootstrap "postgres" database for DDL; the mirror table lives in
	// the public schema so it cannot collide with the production index in the
	// inventory schema.
	adminDSN := pkgPG.adminDSN()

	setupConn, err := pgx.Connect(ctx, adminDSN)
	require.NoError(t, err)
	defer func() { _ = setupConn.Close(ctx) }()

	tableName := "public.stocks_occ_race_" + sanitizeUUIDForIdent(uuid.NewString())

	// Drop any orphaned tables from previous failed runs.
	_, err = setupConn.Exec(ctx, `
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
		END $$;`)
	require.NoError(t, err)

	_, err = setupConn.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s (
		id uuid PRIMARY KEY,
		tenant_id uuid NOT NULL,
		repository_id uuid NOT NULL,
		item_id uuid NOT NULL,
		version bigint NOT NULL
	)`, tableName))
	require.NoError(t, err)
	t.Cleanup(func() {
		dropCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = setupConn.Exec(dropCtx, "DROP TABLE IF EXISTS "+tableName+" CASCADE")
	})

	// The index carries the production name: wrapOCCConflict matches on exactly
	// this string, so naming it anything else would defeat the purpose.
	_, err = setupConn.Exec(ctx, fmt.Sprintf(
		`CREATE UNIQUE INDEX %s ON %s (tenant_id, repository_id, item_id, version)`,
		stockOCCUniqueIndex, tableName))
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

			workerConn, connErr := pgx.Connect(ctx, adminDSN)
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
