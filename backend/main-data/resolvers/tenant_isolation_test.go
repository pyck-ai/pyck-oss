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
		Tenants   []tenantDef    `yaml:"tenants"`
		Customers []customerSeed `yaml:"customers"`
		Suppliers []supplierSeed `yaml:"suppliers"`
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
)

type (
	customerSeed struct {
		Name   string `yaml:"name"`
		Tenant string `yaml:"tenant"`
	}

	supplierSeed struct {
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
			seedCustomers(t, te, scenario.Seed.Customers, ids)
			seedSuppliers(t, te, scenario.Seed.Suppliers, ids)

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

func seedCustomers(t *testing.T, te *testEnv, customers []customerSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, cs := range customers {
		user := mustTenantUser(t, cs.Tenant)
		key := cs.Tenant + "/" + cs.Name
		ids[key] = te.newCustomer(te.ctx(user), user).Create().ID
		t.Logf("Seeded customer: %s (id=%s)", key, ids[key])
	}
}

func seedSuppliers(t *testing.T, te *testEnv, suppliers []supplierSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, ss := range suppliers {
		user := mustTenantUser(t, ss.Tenant)
		key := ss.Tenant + "/" + ss.Name
		ids[key] = te.newSupplier(te.ctx(user), user).Create().ID
		t.Logf("Seeded supplier: %s (id=%s)", key, ids[key])
	}
}

// =============================================================================
// CHECK HANDLERS
// =============================================================================

func checkQuery(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck) {
	t.Helper()

	switch check.Entity {
	case "customer":
		data := execOK[queryCustomersData](te, ctx, queryCustomers, nil)
		assert.Equal(t, check.ExpectCount, data.Customers.TotalCount)
	case "supplier":
		data := execOK[querySuppliersData](te, ctx, querySuppliers, nil)
		assert.Equal(t, check.ExpectCount, data.Suppliers.TotalCount)
	default:
		t.Fatalf("unsupported entity %q for query check", check.Entity)
	}
}

func checkCrossUpdate(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	targetID := mustResolve(t, ids, check.Target)

	switch check.Entity {
	case "customer":
		execErr(te, ctx, updateCustomer, map[string]any{
			"ID":         targetID,
			"DataTypeID": dataTypeIDTenantA,
		}, check.ExpectError)
	case "supplier":
		execErr(te, ctx, updateSupplier, map[string]any{
			"ID":         targetID,
			"DataTypeID": dataTypeIDTenantA,
		}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-update check", check.Entity)
	}
}

func checkCrossDelete(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	targetID := mustResolve(t, ids, check.Target)

	switch check.Entity {
	case "customer":
		execErr(te, ctx, deleteCustomer, map[string]any{"ID": targetID}, check.ExpectError)
	case "supplier":
		execErr(te, ctx, deleteSupplier, map[string]any{"ID": targetID}, check.ExpectError)
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
