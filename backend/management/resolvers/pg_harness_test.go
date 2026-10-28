// Real-Postgres checks of the management resolvers, on the shared
// backend/common/test/pgtest container harness (TestMain in
// resolver_test.go starts it). The rest of the package runs on SQLite;
// the tests here cover SQL whose meaning depends on Postgres itself.
// SKIP_PG_TESTS=1 skips them.
package resolvers_test

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/pgtest"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/validator"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	enthook "github.com/pyck-ai/pyck/backend/management/ent/gen/hook"
	entmigrate "github.com/pyck-ai/pyck/backend/management/ent/migrate"
	"github.com/pyck-ai/pyck/backend/management/ent/types"
	"github.com/pyck-ai/pyck/backend/management/resolvers"
)

// pkgPG is the shared Postgres container of this test binary; nil when
// SKIP_PG_TESTS=1, which makes every test using it skip.
var pkgPG *pgtest.Handle

// openPGDB allocates an isolated database with every management migration
// applied, so the tenant name index is the one production has.
func openPGDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := pgtest.CreateMigratedDB(t, pkgPG, "management", entmigrate.Migrations)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

// The registerTenant pre-flight compares names by name_key and counts
// soft-deleted rows. On Postgres an index-caught 23505 gives the same
// "already exists" with no row, so only the absence of an insert attempt
// shows that the pre-flight itself matched: a pre-flight that silently
// matched nothing would still be answered "already exists", by the index.
func TestRegisterTenant_PostgresPreflightRefusesCaseVariantsOfAnyRow(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		existing string
		deleted  bool
		register string
	}{
		{"soft-deleted row, upper case", "Acme", true, "ACME"},
		{"soft-deleted row, same case", "Acme", true, "Acme"},
		{"live row, mixed case", "Acme", false, "aCmE"},
		// lower() under the database's libc locale keeps these apart;
		// Zitadel's Go fold, and so name_key, does not.
		{"live row, circled letters", "\u24b6cme-x", false, "\u24d0cme-x"},
		{"soft-deleted row, circled letters", "\u24b6cme-x", true, "\u24d0CME-X"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := openPGDB(t)
			ctx := t.Context()

			var deletedAt any
			if tc.deleted {
				deletedAt = time.Now().UTC()
			}
			id := uuid.New()
			_, err := db.ExecContext(ctx,
				`INSERT INTO tenants (id, created_at, created_by, name, name_key, idp_org_ref, deleted_at) VALUES ($1, now(), $2, $3, $4, $5, $6)`,
				id, uuid.Max, tc.existing, types.TenantNameKey(tc.existing), id.String(), deletedAt)
			require.NoError(t, err)

			client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			var creates atomic.Int32
			client.Tenant.Use(enthook.On(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
					creates.Add(1)
					return next.Mutate(ctx, m)
				})
			}, ent.OpCreate))

			r := resolvers.NewResolver("management", client, validator.NewValidator(nil), nil, nil, &fakeStreams{})
			input := registerTenantInput
			input.Name = tc.register
			resp, err := r.Mutation().RegisterTenant(request.Context(ctx, userA, resolver.TenantA), input)

			require.ErrorIs(t, err, resolvers.ErrTenantExists)
			assert.Nil(t, resp)
			assert.Zero(t, creates.Load(), "the pre-flight must refuse before the insert; the index alone gives the same answer")

			var n int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM tenants`).Scan(&n))
			assert.Equal(t, 1, n, "no row is added")
		})
	}
}

// A real Postgres failure outside the registration run (the name
// pre-flight, the pending insert, the commit of the pending row) carries
// the driver's text: the relation, the column, the SQLSTATE. registerTenant
// answers it with the fixed internal text instead and writes no row. The
// schema is broken on purpose (a renamed column) so the driver's own error
// is the one under test, not a constructed one.
func TestRegisterTenant_PostgresFailureOutsideTheRunIsAFixedAnswer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// breakSchema runs against the test's database before the call.
		breakSchema string
		// abortTx, when set, fails the pending row's transaction after a
		// successful insert, so the commit is the call that fails.
		abortTx bool
		leaks   []string
	}{
		{
			name:        "the name pre-flight fails",
			breakSchema: `ALTER TABLE tenants RENAME COLUMN name_key TO name_key_gone`,
			leaks:       []string{"name_key", "42703", "column"},
		},
		{
			name:        "the pending insert fails",
			breakSchema: `ALTER TABLE tenants RENAME COLUMN registration TO registration_gone`,
			leaks:       []string{"registration\"", "42703", "column", "tenants"},
		},
		{
			name:    "the pending row's commit fails",
			abortTx: true,
			leaks:   []string{"commit", "rollback", "22012", "division"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := openPGDB(t)
			ctx := t.Context()
			if tc.breakSchema != "" {
				_, err := db.ExecContext(ctx, tc.breakSchema)
				require.NoError(t, err)
			}

			client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			if tc.abortTx {
				client.Tenant.Use(enthook.On(func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
						v, err := next.Mutate(ctx, m)
						if err != nil {
							return v, err
						}
						tx := ent.TxFromContext(ctx)
						require.NotNil(t, tx, "the pending insert runs inside its own transaction")
						// A failed statement leaves the transaction aborted;
						// the hook swallows its error so only the commit
						// reports it.
						_, execErr := tx.ExecContext(ctx, `SELECT 1/0`)
						require.Error(t, execErr, "the statement must fail to abort the transaction")
						return v, nil
					})
				}, ent.OpCreate))
			}

			r := resolvers.NewResolver("management", client, validator.NewValidator(nil), nil, nil, &fakeStreams{})
			resp, err := r.Mutation().RegisterTenant(request.Context(ctx, userA, resolver.TenantA), registerTenantInput)

			assert.Nil(t, resp)
			require.ErrorIs(t, err, resolvers.ErrRegistrationInternal)
			require.NotErrorIs(t, err, resolvers.ErrTenantExists)
			assert.Equal(t, resolvers.ErrRegistrationInternal.Error()+`: tenant "Test Tenant"`, err.Error())
			for _, leak := range append([]string{"pq:", "pgx", "SQLSTATE", "ERROR"}, tc.leaks...) {
				assert.NotContains(t, err.Error(), leak)
			}

			var n int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM tenants`).Scan(&n))
			assert.Zero(t, n, "no row is left behind")
		})
	}
}
