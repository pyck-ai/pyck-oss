// Real-Postgres checks of the management migrations, on the shared
// backend/common/test/pgtest container harness. SKIP_PG_TESTS=1 skips them.
package migrate_test

import (
	"context"
	"database/sql"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"unicode"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/pyck-ai/pyck/backend/common/authn"
	commondb "github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/test/pgtest"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	_ "github.com/pyck-ai/pyck/backend/management/ent/gen/runtime" // schema hooks for the Ent client below
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	entmigrate "github.com/pyck-ai/pyck/backend/management/ent/migrate"
	"github.com/pyck-ai/pyck/backend/management/ent/types"
)

var pkgPG *pgtest.Handle

// TestMain starts the shared Postgres container and fails the binary when a
// goroutine a test started outlives the tests (goleak, no ignores); the
// container is terminated inside Run so the check sees only the tests'
// leftovers.
func TestMain(m *testing.M) {
	var terminate func()
	pkgPG, terminate = pgtest.Start("management/ent/migrate")
	goleak.VerifyTestMain(pgtest.RunThen(m, terminate))
}

// migratedDB returns a database with every management migration applied.
func migratedDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := pgtest.CreateMigratedDB(t, pkgPG, "management", entmigrate.Migrations)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

// insertTenant writes one tenant row directly, with the name_key the
// schema hook would derive; deleted soft-deletes it.
func insertTenant(ctx context.Context, db *sql.DB, name string, deleted bool) error {
	id := uuid.New()
	var deletedAt any
	if deleted {
		deletedAt = time.Now().UTC()
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO tenants (id, created_at, created_by, name, name_key, idp_org_ref, deleted_at) VALUES ($1, now(), $2, $3, $4, $5, $6)`,
		id, uuid.Max, name, types.TenantNameKey(name), id.String(), deletedAt)
	return err
}

// requireNameViolation asserts err is the unique violation of the
// name_key index, the 23505 registerTenant maps to "already exists".
func requireNameViolation(t *testing.T, err error, name string) {
	t.Helper()
	require.Error(t, err, "%q must be refused", name)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "23505", pgErr.Code)
	assert.Equal(t, "tenant_name_key", pgErr.ConstraintName)
}

// Tenant names are unique by name_key over every row, soft-deleted ones
// included: Zitadel compares org names without case, and a retired tenant
// keeps its name so restoreTenant can always bring it back.
func TestTenantNameIndex_RefusesCaseVariantsAcrossAllRows(t *testing.T) {
	t.Parallel()
	db := migratedDB(t)
	ctx := t.Context()

	require.NoError(t, insertTenant(ctx, db, "Acme", false))
	require.NoError(t, insertTenant(ctx, db, "Retired", true))

	for _, name := range []string{"Acme", "acme", "ACME", "aCmE"} {
		requireNameViolation(t, insertTenant(ctx, db, name, false), name)
	}
	for _, name := range []string{"Retired", "retired", "RETIRED"} {
		requireNameViolation(t, insertTenant(ctx, db, name, false), name)
		requireNameViolation(t, insertTenant(ctx, db, name, true), name+" (soft-deleted)")
	}

	require.NoError(t, insertTenant(ctx, db, "Acme Two", false), "a different name is accepted")
	var n int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM tenants`).Scan(&n))
	assert.Equal(t, 3, n)
}

// The name_key index is unique over every row (no partial WHERE), and the
// partial case-sensitive index it replaces is gone.
func TestTenantNameIndex_IsUniqueOverEveryRow(t *testing.T) {
	t.Parallel()
	db := migratedDB(t)

	var def string
	require.NoError(t, db.QueryRowContext(t.Context(),
		`SELECT indexdef FROM pg_indexes WHERE schemaname = 'management' AND indexname = 'tenant_name_key'`).Scan(&def))
	assert.Equal(t, "CREATE UNIQUE INDEX tenant_name_key ON management.tenants USING btree (name_key)", def)

	var partial int
	require.NoError(t, db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM pg_indexes WHERE schemaname = 'management' AND indexname = 'tenant_name'`).Scan(&partial))
	assert.Zero(t, partial, "the case-sensitive partial index must be dropped")
}

// registerTenant maps the violation to its friendly refusal with
// ent.IsConstraintError; on Postgres the case-variant insert through Ent
// must surface as exactly that.
func TestTenantNameIndex_CaseVariantIsAnEntConstraintError(t *testing.T) {
	t.Parallel()
	db := migratedDB(t)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	ctx := authn.Context(t.Context(), authn.SystemUser())

	create := func(name string) error {
		id := uuid.New()
		return client.Tenant.Create().SetID(id).SetName(name).SetIdpOrgRef(id.String()).
			SetStatus(enttenant.StatusPending).Exec(ctx)
	}
	require.NoError(t, create("Test Tenant"))

	err := create("TEST TENANT")
	require.Error(t, err)
	assert.True(t, ent.IsConstraintError(err), "want an Ent constraint error, got %T: %v", err, err)
}

// nameKeyMigration is the migration that adds name_key, backfills it and
// puts the tenant name index on it.
const nameKeyMigration = "20261009144936"

// migrationsBefore returns the management migrations older than version,
// laid out the way commondb.RunMigrations reads them.
func migrationsBefore(t *testing.T, version string) fstest.MapFS {
	t.Helper()
	entries, err := fs.ReadDir(entmigrate.Migrations, "migrations")
	require.NoError(t, err)
	out := fstest.MapFS{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") || e.Name() >= version {
			continue
		}
		data, err := fs.ReadFile(entmigrate.Migrations, "migrations/"+e.Name())
		require.NoError(t, err)
		out["migrations/"+e.Name()] = &fstest.MapFile{Data: data}
	}
	require.NotEmpty(t, out)
	return out
}

// legacyDB returns a database migrated up to just before the name_key
// migration, and its DSN for applying the rest.
func legacyDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := pgtest.CreateMigratedDB(t, pkgPG, "management", migrationsBefore(t, nameKeyMigration))
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db, dsn
}

// insertLegacyTenant writes a tenant row the way the schema before name_key
// held it, created age ago, and returns its id.
func insertLegacyTenant(t *testing.T, db *sql.DB, name string, deleted bool, age time.Duration) uuid.UUID {
	t.Helper()
	id := uuid.New()
	var deletedAt any
	if deleted {
		deletedAt = time.Now().UTC()
	}
	_, err := db.ExecContext(t.Context(),
		`INSERT INTO tenants (id, created_at, created_by, name, idp_org_ref, deleted_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		id, time.Now().UTC().Add(-age), uuid.Max, name, id.String(), deletedAt)
	require.NoError(t, err)
	return id
}

// migrateRest applies every migration still pending on dsn.
func migrateRest(t *testing.T, dsn string) error {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	return commondb.RunMigrations(t.Context(), db, "management", entmigrate.Migrations)
}

// Rows that exist before the release get the key the Go code would have
// written for them, soft-deleted ones included (the circled letters and
// the non-ASCII case pairs Postgres' libc lower() gets wrong among them)
// so the index compares old and new rows by the same fold.
func TestNameKeyMigration_BackfillsExistingRowsWithTheGoFold(t *testing.T) {
	t.Parallel()
	db, dsn := legacyDB(t)

	names := map[string]bool{
		"Acme":           false,
		"Ⓐcme-x":         false,
		"ÄRZTE GmbH":     false,
		"İstanbul":       false,
		"ΣΑΣ":            false,
		"Retired Tenant": true,
		"Straße & Söhne": true,
	}
	for name, deleted := range names {
		insertLegacyTenant(t, db, name, deleted, 0)
	}

	require.NoError(t, migrateRest(t, dsn))

	rows, err := db.QueryContext(t.Context(), `SELECT name, name_key FROM tenants`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	got := map[string]string{}
	for rows.Next() {
		var name, key string
		require.NoError(t, rows.Scan(&name, &key))
		got[name] = key
	}
	require.NoError(t, rows.Err())
	require.Len(t, got, len(names))
	for name := range names {
		assert.Equal(t, types.TenantNameKey(name), got[name], "backfilled key of %q", name)
	}

	// The backfilled keys are enforced like new ones: the circled variant
	// of a pre-existing name is now refused.
	requireNameViolation(t, insertTenant(t.Context(), db, "ⓐCME-X", false), "ⓐCME-X")
}

// Existing rows that fold to one key predate the index and cannot all keep
// it, so the migration resolves them instead of failing the deploy: the
// live row, then the oldest, keeps the plain key, every other one gets
// "#<id>" appended. No row is renamed, and the name stays taken for new
// registrations.
func TestNameKeyMigration_ResolvesExistingFoldedDuplicates(t *testing.T) {
	t.Parallel()

	type row struct {
		name    string
		deleted bool
		age     time.Duration
	}
	cases := map[string]struct {
		rows []row
		kept int // index of the row that keeps the plain key
	}{
		"circled letters, both live, the older keeps it": {
			rows: []row{{"ⓐcme", false, time.Hour}, {"Ⓐcme", false, 2 * time.Hour}},
			kept: 1,
		},
		"case variants, the live one keeps it over an older retired one": {
			rows: []row{{"Acme", true, 2 * time.Hour}, {"ACME", false, time.Hour}},
			kept: 1,
		},
		"three rows, the oldest live one keeps it": {
			rows: []row{{"acme", false, time.Hour}, {"Acme", false, 3 * time.Hour}, {"ACME", true, 4 * time.Hour}},
			kept: 1,
		},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			db, dsn := legacyDB(t)
			ids := make([]uuid.UUID, len(tc.rows))
			for i, r := range tc.rows {
				ids[i] = insertLegacyTenant(t, db, r.name, r.deleted, r.age)
			}

			require.NoError(t, migrateRest(t, dsn))

			for i, r := range tc.rows {
				var name, key string
				require.NoError(t, db.QueryRowContext(t.Context(),
					`SELECT name, name_key FROM tenants WHERE id = $1`, ids[i]).Scan(&name, &key))
				assert.Equal(t, r.name, name, "the migration renames no row")
				want := types.TenantNameKey(r.name)
				if i != tc.kept {
					want += "#" + ids[i].String()
				}
				assert.Equal(t, want, key, "name_key of %q", r.name)
			}
			probe := strings.ToUpper(tc.rows[tc.kept].name)
			requireNameViolation(t, insertTenant(t.Context(), db, probe, false), probe)
		})
	}
}

// newerThanPostgresUnicode are the capitals Go's Unicode 17 tables
// lowercase and a supported Postgres may not know a case mapping for.
// PostgreSQL 17 (Unicode 15.1, the production major) misses all 55;
// PostgreSQL 18 (Unicode 16, the test image) misses U+A7CE, U+A7D2,
// U+A7D4 and the Beria Erfe capitals U+16EA0..U+16EB8, added in Unicode
// 17. The name_key migration warns about any row whose backfilled key still
// holds one of them; its character class must cover this whole set.
var newerThanPostgresUnicode = func() map[rune]bool {
	set := map[rune]bool{}
	for _, r := range []rune{0x1C89, 0xA7CB, 0xA7CC, 0xA7CE, 0xA7D2, 0xA7D4, 0xA7DA, 0xA7DC} {
		set[r] = true
	}
	for r := rune(0x10D50); r <= 0x10D65; r++ {
		set[r] = true
	}
	for r := rune(0x16EA0); r <= 0x16EB8; r++ {
		set[r] = true
	}
	return set
}()

// A row whose name holds a capital the server's Unicode tables do not
// fold would be backfilled with a key the Go code never writes: an
// identical-name registration would compute the folded key, pass both the
// pre-flight and the index, and end as an error row once Zitadel refuses
// the org name. The migration only warns about such a row and goes on: it
// keeps the server's key, while every capital the server folds gets the
// Go key.
func TestNameKeyMigration_KeepsNamesTheServerCannotFold(t *testing.T) {
	t.Parallel()
	db, dsn := legacyDB(t)
	ctx := t.Context()

	var unfolded, folded []string
	for r := range newerThanPostgresUnicode {
		require.NotEqual(t, string(r), types.TenantNameKey(string(r)), "%U: Go must fold every capital in the set", r)
		name := "Legacy " + string(r)
		insertLegacyTenant(t, db, name, false, 0)
		var serverFolds bool
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT lower(chr($1) COLLATE pg_c_utf8) <> chr($1)`, r).Scan(&serverFolds))
		if serverFolds {
			folded = append(folded, name)
		} else {
			unfolded = append(unfolded, name)
		}
	}
	require.NotEmpty(t, unfolded, "the test image folds every capital in the set; pin a server that does not")

	require.NoError(t, migrateRest(t, dsn))

	keys := map[string]string{}
	rows, err := db.QueryContext(ctx, `SELECT name, name_key FROM tenants`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	for rows.Next() {
		var name, key string
		require.NoError(t, rows.Scan(&name, &key))
		keys[name] = key
	}
	require.NoError(t, rows.Err())
	for _, name := range unfolded {
		assert.NotEqual(t, types.TenantNameKey(name), keys[name], "%q keeps the server's key", name)
	}
	for _, name := range folded {
		assert.Equal(t, types.TenantNameKey(name), keys[name], "a capital the server folds is backfilled correctly")
	}
}

// The backfill folds with lower(name COLLATE pg_c_utf8), new rows with
// types.TenantNameKey. Over every code point the two agree except where
// Postgres' Unicode tables predate a case pair Go knows; there Postgres
// leaves the code point as it is, never folds it to something else, and
// the migration warns about the row (see the test above). A Postgres or Go
// upgrade that adds a disagreement fails here, which is the moment to add
// the code point to the set and to the migration's character class.
func TestNameKeyMigration_BackfillFoldAgreesWithGoBarNewerUnicode(t *testing.T) {
	t.Parallel()
	db := migratedDB(t)

	rows, err := db.QueryContext(t.Context(), `
		SELECT cp, lower(chr(cp) COLLATE pg_c_utf8)
		FROM generate_series(1, 1114111) AS cp
		WHERE (cp < 55296 OR cp > 57343) AND lower(chr(cp) COLLATE pg_c_utf8) <> chr(cp)`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	pgFold := map[rune]string{}
	for rows.Next() {
		var cp int32
		var folded string
		require.NoError(t, rows.Scan(&cp, &folded))
		pgFold[cp] = folded
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, pgFold)

	disagree := map[rune]bool{}
	for r := rune(1); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		s := string(r)
		pg, ok := pgFold[r]
		if !ok {
			pg = s
		}
		if goKey := types.TenantNameKey(s); goKey != pg {
			disagree[r] = true
			assert.Equal(t, s, pg, "%U: Postgres folds to %q, Go to %q; only an unknown mapping may differ", r, pg, goKey)
		}
	}
	for r := range disagree {
		assert.True(t, newerThanPostgresUnicode[r], "%U: a new disagreement between the backfill fold and types.TenantNameKey", r)
	}
}
