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
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
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
		Tenants   []tenantDef    `yaml:"tenants"`
		Workflows []workflowSeed `yaml:"workflows"`
	}

	tenantDef struct {
		Name string `yaml:"name"`
	}

	tenantCheck struct {
		Type        string `yaml:"type"`
		Entity      string `yaml:"entity"`
		Tenant      string `yaml:"tenant"`
		Target      string `yaml:"target"`
		Description string `yaml:"description"`
		ExpectCount int    `yaml:"expectCount"`
		ExpectError string `yaml:"expectError"`
	}

	workflowSeed struct {
		Name   string `yaml:"name"`
		Tenant string `yaml:"tenant"`
	}
)

// =============================================================================
// TENANT FIXTURES
// =============================================================================

// userB is not defined in resolver_test.go, so we define it here for
// cross-tenant testing.
var userB = &authn.User{
	ID:       uuid.MustParse("509e889e-6983-491f-86b1-267174288fef"),
	TenantID: resolver.TenantB,
	Roles:    map[uuid.UUID]authn.Role{resolver.TenantB: authn.ROLE_ADMIN},
}

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
			seedWorkflows(t, te, scenario.Seed.Workflows, ids)

			// Check phase.
			for _, check := range scenario.Checks {
				t.Run(check.Description, func(t *testing.T) {
					ctx := te.ctx(mustTenantUser(t, check.Tenant))

					switch check.Type {
					case "query":
						checkQuery(t, te, ctx, check)
					case "cross-delete":
						checkCrossDelete(t, te, ctx, check, ids)
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

func seedWorkflows(t *testing.T, te *testEnv, workflows []workflowSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, ws := range workflows {
		user := mustTenantUser(t, ws.Tenant)
		key := ws.Tenant + "/" + ws.Name
		ids[key] = te.newWorkflow(te.ctx(user), user).Name(ws.Name).Create().ID
		t.Logf("Seeded workflow: %s (id=%s)", key, ids[key])
	}
}

// =============================================================================
// CHECK HANDLERS
// =============================================================================

func checkQuery(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck) {
	t.Helper()

	switch check.Entity {
	case "workflow":
		data := execOK[queryWorkflowsData](te, ctx, queryWorkflows, nil)
		assert.Equal(t, check.ExpectCount, data.Workflows.TotalCount)
	default:
		t.Fatalf("unsupported entity %q for query check", check.Entity)
	}
}

func checkCrossDelete(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	targetID := mustResolve(t, ids, check.Target)

	switch check.Entity {
	case "workflow":
		execErr(te, ctx, deleteWorkflow, map[string]any{"ID": targetID}, check.ExpectError)
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
