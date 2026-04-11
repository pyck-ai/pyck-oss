package resolvers_test

import (
	"context"
	"embed"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/pyck-ai/pyck/backend/common/authn"
)

//go:embed testdata/tenant-isolation/*.test.yaml
var tenantTestdata embed.FS

// =============================================================================
// TYPES
// =============================================================================

type (
	tenantScenario struct {
		Name        string        `yaml:"name"`
		Description string        `yaml:"description"`
		Seed        tenantSeed    `yaml:"seed"`
		Checks      []tenantCheck `yaml:"checks"`
	}

	tenantSeed struct {
		Tenants []tenantDef `yaml:"tenants"`
		Files   []fileSeed  `yaml:"files"`
	}

	tenantDef struct {
		Name string `yaml:"name"`
	}

	tenantCheck struct {
		Type        string         `yaml:"type"`
		Entity      string         `yaml:"entity"`
		Tenant      string         `yaml:"tenant"`
		Target      string         `yaml:"target"`
		Description string         `yaml:"description"`
		ExpectCount int            `yaml:"expectCount"`
		ExpectError string         `yaml:"expectError"`
		Args        map[string]any `yaml:"args"`
	}

	fileSeed struct {
		Name   string `yaml:"name"`
		Tenant string `yaml:"tenant"`
	}
)

// =============================================================================
// TENANT FIXTURES
// =============================================================================

var tenantUsers = map[string]*authn.User{
	"alpha": userA,
	"beta":  userB,
}

// =============================================================================
// TEST RUNNER
// =============================================================================

func TestTenantIsolation(t *testing.T) {
	t.Parallel()

	scenarios := loadTenantScenarios(t)

	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)

			for _, td := range scenario.Seed.Tenants {
				_ = mustTenantUser(t, td.Name)
			}

			ids := make(map[string]uuid.UUID)

			// Seed phase.
			seedFiles(t, te, scenario.Seed.Files, ids)

			// Check phase.
			for _, check := range scenario.Checks {
				t.Run(check.Description, func(t *testing.T) {
					ctx := te.ctx(mustTenantUser(t, check.Tenant))

					switch check.Type {
					case "query":
						checkQuery(t, te, ctx, check)
					case "cross-update":
						checkCrossUpdate(t, te, ctx, check, ids)
					case "cross-delete":
						checkCrossDelete(t, te, ctx, check, ids)
					case "cross-create":
						checkCrossCreate(t, te, ctx, check, ids)
					default:
						t.Fatalf("unknown check type %q", check.Type)
					}
				})
			}
		})
	}
}

// =============================================================================
// SEED HELPERS
// =============================================================================

func seedFiles(t *testing.T, te *testEnv, files []fileSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, fs := range files {
		user := mustTenantUser(t, fs.Tenant)
		key := fs.Tenant + "/" + fs.Name
		ids[key] = te.newFile(te.ctx(user), user).Name(fs.Name).Create().ID
		t.Logf("Seeded file: %s (id=%s)", key, ids[key])
	}
}

// =============================================================================
// CHECK HANDLERS
// =============================================================================

func checkQuery(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck) {
	t.Helper()

	switch check.Entity {
	case "file":
		data := execOK[queryFilesData](te, ctx, queryFiles, nil)
		assert.Equal(t, check.ExpectCount, data.Files.TotalCount)
	default:
		t.Fatalf("unsupported entity %q for query check", check.Entity)
	}
}

func checkCrossUpdate(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	targetID := mustResolve(t, ids, check.Target)

	switch check.Entity {
	case "file":
		execErr(te, ctx, updateFile, map[string]any{
			"ID":          targetID,
			"Description": "cross-tenant-test",
		}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-update check", check.Entity)
	}
}

func checkCrossCreate(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()

	switch check.Entity {
	case "file":
		execErr(te, ctx, createFile, map[string]any{
			"RefID":       mustResolve(t, ids, mustArg(t, check.Args, "refid")),
			"RefType":     testRefType,
			"Name":        "cross-tenant-file.txt",
			"Size":        100,
			"ContentType": "text/plain",
			"DataTypeID":  fileDataTypeID,
		}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-create check", check.Entity)
	}
}

func checkCrossDelete(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	targetID := mustResolve(t, ids, check.Target)

	switch check.Entity {
	case "file":
		execErr(te, ctx, deleteFile, map[string]any{"ID": targetID}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-delete check", check.Entity)
	}
}

// =============================================================================
// PRIVATE HELPERS
// =============================================================================

func mustTenantUser(t *testing.T, name string) *authn.User {
	t.Helper()
	user, ok := tenantUsers[name]
	require.True(t, ok, "unknown tenant %q", name)
	return user
}

func mustArg(t *testing.T, args map[string]any, key string) string {
	t.Helper()
	v, ok := args[key]
	require.True(t, ok, "missing arg %q", key)
	s, ok := v.(string)
	require.True(t, ok, "arg %q must be string, got %T", key, v)
	return s
}

func mustResolve(t *testing.T, ids map[string]uuid.UUID, key string) uuid.UUID {
	t.Helper()
	id, ok := ids[key]
	require.True(t, ok, "entity %q not found (available: %v)", key, mapKeys(ids))
	return id
}

func mapKeys(m map[string]uuid.UUID) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func loadTenantScenarios(t *testing.T) []tenantScenario {
	t.Helper()

	entries, err := tenantTestdata.ReadDir("testdata/tenant-isolation")
	require.NoError(t, err, "failed to read testdata/tenant-isolation directory")

	var scenarios []tenantScenario
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".test.yaml") {
			continue
		}
		data, err := tenantTestdata.ReadFile(filepath.Join("testdata/tenant-isolation", entry.Name()))
		require.NoError(t, err, "failed to read %s", entry.Name())

		var scenario tenantScenario
		err = yaml.Unmarshal(data, &scenario)
		require.NoError(t, err, "failed to parse %s", entry.Name())

		if scenario.Name == "" {
			scenario.Name = strings.TrimSuffix(entry.Name(), ".test.yaml")
		}

		scenarios = append(scenarios, scenario)
		t.Logf("Loaded tenant scenario: %s (%s)", scenario.Name, entry.Name())
	}

	require.NotEmpty(t, scenarios, "no tenant test scenarios found")
	return scenarios
}
