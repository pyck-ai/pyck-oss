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
	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"
)

//go:embed testdata/tenant-isolation/*.test.yaml
var tenantTestdata embed.FS

// =============================================================================
// GRAPHQL TEMPLATES (entities without existing test templates)
// =============================================================================

var (
	updateUser = testresolver.ParseTemplate(`mutation {
		updateUser(id: "{{.ID}}", input: {
			{{if .Username}}username: "{{.Username}}"{{end}}
		}) { id tenantID username }
	}`)

	deleteUser = testresolver.ParseTemplate(`mutation {
		deleteUser(id: "{{.ID}}")
	}`)

	queryUsers = testresolver.ParseTemplate(`query {
		users { totalCount edges { node { id tenantID username } } }
	}`)

	queryDeviceUsers = testresolver.ParseTemplate(`query {
		deviceUsers { totalCount edges { node { id deviceID userID } } }
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type (
	queryDeviceUsersData struct {
		DeviceUsers struct {
			TotalCount int
			Edges      []struct{ Node deviceUserNode }
		}
	}

	userNode struct {
		ID       uuid.UUID
		TenantID uuid.UUID
		Username string
	}
	queryUsersData struct {
		Users struct {
			TotalCount int
			Edges      []struct{ Node userNode }
		}
	}
)

// =============================================================================
// YAML STRUCTURES
// =============================================================================

type (
	tenantScenario struct {
		Name        string        `yaml:"name"`
		Description string        `yaml:"description"`
		Seed        tenantSeed    `yaml:"seed"`
		Checks      []tenantCheck `yaml:"checks"`
	}

	tenantSeed struct {
		Tenants         []tenantDef          `yaml:"tenants"`
		DataTypes       []dataTypeSeed       `yaml:"dataTypes"`
		Locations       []locationSeed       `yaml:"locations"`
		Devices         []deviceSeed         `yaml:"devices"`
		DeviceLocations []deviceLocationSeed `yaml:"deviceLocations"`
		Users           []userSeed           `yaml:"users"`
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
)

type (
	dataTypeSeed struct {
		Name   string `yaml:"name"`
		Tenant string `yaml:"tenant"`
	}

	locationSeed struct {
		Name   string `yaml:"name"`
		Tenant string `yaml:"tenant"`
	}

	deviceSeed struct {
		Name   string `yaml:"name"`
		Tenant string `yaml:"tenant"`
	}

	deviceLocationSeed struct {
		Name     string `yaml:"name"`
		Tenant   string `yaml:"tenant"`
		Device   string `yaml:"device"`
		Location string `yaml:"location"`
	}

	userSeed struct {
		Name       string `yaml:"name"`
		Tenant     string `yaml:"tenant"`
		AsAuthUser bool   `yaml:"asAuthUser"`
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

			// Seed Tenant DB records (User entity has FK to tenants table).
			for _, td := range scenario.Seed.Tenants {
				user := mustTenantUser(t, td.Name)
				te.newTenant(te.ctx(user), user.TenantID).Create()
				t.Logf("Seeded tenant record: %s (id=%s)", td.Name, user.TenantID)
			}

			// Seed phase (order matters for FK dependencies).
			seedDataTypes(t, te, scenario.Seed.DataTypes, ids)
			seedLocations(t, te, scenario.Seed.Locations, ids)
			seedDevices(t, te, scenario.Seed.Devices, ids)
			seedDeviceLocations(t, te, scenario.Seed.DeviceLocations, ids)
			seedUsers(t, te, scenario.Seed.Users, ids)
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
					case "cross-checkin":
						checkCrossCheckIn(t, te, ctx, check, ids)
					case "cross-checkout":
						checkCrossCheckOut(t, te, ctx, check, ids)
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

func seedDataTypes(t *testing.T, te *testEnv, dts []dataTypeSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, ds := range dts {
		user := mustTenantUser(t, ds.Tenant)
		key := ds.Tenant + "/" + ds.Name
		ids[key] = te.newDataType(te.ctx(user), user).Name(ds.Name).Slug(ds.Name).Create().ID
		t.Logf("Seeded data type: %s (id=%s)", key, ids[key])
	}
}

func seedLocations(t *testing.T, te *testEnv, locs []locationSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, ls := range locs {
		user := mustTenantUser(t, ls.Tenant)
		key := ls.Tenant + "/" + ls.Name
		ids[key] = te.newLocation(te.ctx(user), user).Name(ls.Name).Create().ID
		t.Logf("Seeded location: %s (id=%s)", key, ids[key])
	}
}

func seedDevices(t *testing.T, te *testEnv, devs []deviceSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, ds := range devs {
		user := mustTenantUser(t, ds.Tenant)
		key := ds.Tenant + "/" + ds.Name
		ids[key] = te.newDevice(te.ctx(user), user).Name(ds.Name).Create().ID
		t.Logf("Seeded device: %s (id=%s)", key, ids[key])
	}
}

func seedDeviceLocations(t *testing.T, te *testEnv, dls []deviceLocationSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, dl := range dls {
		user := mustTenantUser(t, dl.Tenant)
		deviceID := mustResolve(t, ids, dl.Tenant+"/"+dl.Device)
		locationID := mustResolve(t, ids, dl.Tenant+"/"+dl.Location)
		key := dl.Tenant + "/" + dl.Name
		ids[key] = te.newDeviceLocation(te.ctx(user), user, deviceID, locationID).Create().ID
		t.Logf("Seeded device location: %s (id=%s)", key, ids[key])
	}
}

func seedUsers(t *testing.T, te *testEnv, users []userSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, us := range users {
		user := mustTenantUser(t, us.Tenant)
		b := te.newUser(te.ctx(user), user).Username(us.Name)
		if us.AsAuthUser {
			b.ID(user.ID)
		}
		key := us.Tenant + "/" + us.Name
		ids[key] = b.Create().ID
		t.Logf("Seeded user: %s (id=%s, asAuthUser=%v)", key, ids[key], us.AsAuthUser)
	}
}

// =============================================================================
// CHECK HANDLERS
// =============================================================================

func checkQuery(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck) {
	t.Helper()

	switch check.Entity {
	case "dataType":
		data := execOK[queryDataTypesData](te, ctx, queryDataTypes, nil)
		assert.Equal(t, check.ExpectCount, data.DataTypes.TotalCount)
	case "location":
		data := execOK[queryLocationsData](te, ctx, queryLocationsJSONOrder, nil)
		assert.Equal(t, check.ExpectCount, data.Locations.TotalCount)
	case "device":
		data := execOK[queryDevicesData](te, ctx, queryDevicesJSONOrder, nil)
		assert.Equal(t, check.ExpectCount, data.Devices.TotalCount)
	case "deviceLocation":
		data := execOK[queryDeviceLocationsData](te, ctx, queryDeviceLocationsJSONOrder, nil)
		assert.Equal(t, check.ExpectCount, data.DeviceLocations.TotalCount)
	case "user":
		data := execOK[queryUsersData](te, ctx, queryUsers, nil)
		assert.Equal(t, check.ExpectCount, data.Users.TotalCount)
	case "deviceUser":
		data := execOK[queryDeviceUsersData](te, ctx, queryDeviceUsers, nil)
		assert.Equal(t, check.ExpectCount, data.DeviceUsers.TotalCount)
	default:
		t.Fatalf("unsupported entity %q for query check", check.Entity)
	}
}

func checkCrossUpdate(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	targetID := mustResolve(t, ids, check.Target)

	switch check.Entity {
	case "dataType":
		execErr(te, ctx, updateDataType, map[string]any{
			"ID":          targetID,
			"Name":        "cross-tenant-test",
			"Description": "cross-tenant-test",
			"JsonSchema":  testresolver.EscapeJSON(testDataTypeSchema),
		}, check.ExpectError)
	case "location":
		execErr(te, ctx, updateLocation, map[string]any{"ID": targetID, "Name": "cross-tenant-test"}, check.ExpectError)
	case "device":
		execErr(te, ctx, updateDevice, map[string]any{"ID": targetID, "Name": "cross-tenant-test"}, check.ExpectError)
	case "user":
		execErr(te, ctx, updateUser, map[string]any{"ID": targetID, "Username": "cross-tenant-test"}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-update check", check.Entity)
	}
}

func checkCrossDelete(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	targetID := mustResolve(t, ids, check.Target)

	switch check.Entity {
	case "dataType":
		execErr(te, ctx, deleteDataType, map[string]any{"ID": targetID}, check.ExpectError)
	case "location":
		execErr(te, ctx, deleteLocation, map[string]any{"ID": targetID}, check.ExpectError)
	case "device":
		execErr(te, ctx, deleteDevice, map[string]any{"ID": targetID}, check.ExpectError)
	case "deviceLocation":
		execErr(te, ctx, unsetDeviceLocation, map[string]any{"ID": targetID}, check.ExpectError)
	case "user":
		execErr(te, ctx, deleteUser, map[string]any{"ID": targetID}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-delete check", check.Entity)
	}
}

func checkCrossCreate(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()

	switch check.Entity {
	case "deviceLocation":
		execErr(te, ctx, setDeviceLocation, map[string]any{
			"DeviceID":   mustResolve(t, ids, mustArg(t, check.Args, "device")),
			"LocationID": mustResolve(t, ids, mustArg(t, check.Args, "location")),
		}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-create check", check.Entity)
	}
}

func checkCrossCheckIn(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	execErr(te, ctx, checkInUserDevice, map[string]any{
		"DeviceID": mustResolve(t, ids, mustArg(t, check.Args, "device")),
	}, check.ExpectError)
}

func checkCrossCheckOut(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	execErr(te, ctx, checkOutUserDevice, map[string]any{
		"DeviceID": mustResolve(t, ids, mustArg(t, check.Args, "device")),
	}, check.ExpectError)
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

func mustArg(t *testing.T, args map[string]any, key string) string {
	t.Helper()
	v, ok := args[key]
	require.True(t, ok, "missing arg %q", key)
	s, ok := v.(string)
	require.True(t, ok, "arg %q must be string, got %T", key, v)
	return s
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
