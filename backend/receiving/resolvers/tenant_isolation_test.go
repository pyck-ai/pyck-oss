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
// GRAPHQL TEMPLATES (not defined in other test files)
// =============================================================================

var (
	createInboundWithSupplier = testresolver.ParseTemplate(`mutation {
		createReceivingInbound(input: {
			{{if .SupplierID}}supplierID: "{{.SupplierID}}",{{end}}
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			data: { type: "custom", sum: 15, meta: { name: "Test", weight: 50, tags: ["a", "b"] } }
		}) {
			receivingInbound { id tenantID }
		}
	}`)

	updateInboundWithSupplier = testresolver.ParseTemplate(`mutation {
		updateReceivingInbound(id: "{{.ID}}", input: {
			supplierID: "{{.SupplierID}}"
		}) {
			receivingInbound { id tenantID }
		}
	}`)

	updateItemWithInboundID = testresolver.ParseTemplate(`mutation {
		updateReceivingInboundItem(id: "{{.ID}}", input: {
			inboundID: "{{.InboundID}}"
		}) {
			receivingInboundItem { id tenantID inboundID }
		}
	}`)

	updateNotificationWithInboundID = testresolver.ParseTemplate(`mutation {
		updateReceivingInboundShipmentNotification(id: "{{.ID}}", input: {
			inboundID: "{{.InboundID}}"
		}) {
			receivingInboundShipmentNotification { id tenantID inboundID }
		}
	}`)
)

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
		Tenants                      []tenantDef                       `yaml:"tenants"`
		Inbounds                     []inboundSeed                     `yaml:"inbounds"`
		InboundItems                 []inboundItemSeed                 `yaml:"inboundItems"`
		InboundShipmentNotifications []inboundShipmentNotificationSeed `yaml:"inboundShipmentNotifications"`
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
	inboundSeed struct {
		Name   string `yaml:"name"`
		Tenant string `yaml:"tenant"`
	}

	inboundItemSeed struct {
		Name     string `yaml:"name"`
		Tenant   string `yaml:"tenant"`
		Inbound  string `yaml:"inbound"`
		Sku      string `yaml:"sku"`
		Quantity int64  `yaml:"quantity"`
	}

	inboundShipmentNotificationSeed struct {
		Name    string `yaml:"name"`
		Tenant  string `yaml:"tenant"`
		Inbound string `yaml:"inbound"`
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
			seedInbounds(t, te, scenario.Seed.Inbounds, ids)
			seedInboundItems(t, te, scenario.Seed.InboundItems, ids)
			seedInboundShipmentNotifications(t, te, scenario.Seed.InboundShipmentNotifications, ids)

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
					case "cross-update-with-args":
						checkCrossUpdateWithArgs(t, te, ctx, check, ids)
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

func seedInbounds(t *testing.T, te *testEnv, inbounds []inboundSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, is := range inbounds {
		user := mustTenantUser(t, is.Tenant)
		key := is.Tenant + "/" + is.Name
		ids[key] = te.newInbound(te.ctx(user), user).Create().ID
		t.Logf("Seeded inbound: %s (id=%s)", key, ids[key])
	}
}

func seedInboundShipmentNotifications(t *testing.T, te *testEnv, notifications []inboundShipmentNotificationSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, ns := range notifications {
		user := mustTenantUser(t, ns.Tenant)
		inboundID := mustResolve(t, ids, ns.Tenant+"/"+ns.Inbound)
		notif := te.newNotification(te.ctx(user), user, inboundID).Create()

		key := ns.Tenant + "/" + ns.Name
		ids[key] = notif.ID
		t.Logf("Seeded inbound shipment notification: %s (id=%s)", key, ids[key])
	}
}

func seedInboundItems(t *testing.T, te *testEnv, items []inboundItemSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, is := range items {
		user := mustTenantUser(t, is.Tenant)
		inboundID := mustResolve(t, ids, is.Tenant+"/"+is.Inbound)
		item := te.newItem(te.ctx(user), user, inboundID).
			Sku(is.Sku).
			Quantity(is.Quantity).
			Create()

		key := is.Tenant + "/" + is.Name
		ids[key] = item.ID
		t.Logf("Seeded inbound item: %s (id=%s)", key, ids[key])
	}
}

// =============================================================================
// CHECK HANDLERS
// =============================================================================

func checkQuery(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck) {
	t.Helper()

	switch check.Entity {
	case "inbound":
		data := execOK[queryInboundsData](te, ctx, queryInbounds, nil)
		assert.Equal(t, check.ExpectCount, data.ReceivingInbounds.TotalCount)
	case "inboundItem":
		data := execOK[queryItemsData](te, ctx, queryItems, nil)
		assert.Equal(t, check.ExpectCount, data.ReceivingInboundItems.TotalCount)
	case "inboundShipmentNotification":
		data := execOK[queryNotificationsData](te, ctx, queryNotifications, nil)
		assert.Equal(t, check.ExpectCount, data.ReceivingInboundShipmentNotifications.TotalCount)
	default:
		t.Fatalf("unsupported entity %q for query check", check.Entity)
	}
}

func checkCrossUpdate(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	targetID := mustResolve(t, ids, check.Target)

	switch check.Entity {
	case "inbound":
		execErr(te, ctx, updateInbound, map[string]any{
			"ID":         targetID,
			"OrderID":    "cross-tenant-test",
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		}, check.ExpectError)
	case "inboundItem":
		execErr(te, ctx, updateItem, map[string]any{
			"ID":         targetID,
			"Sku":        "cross-tenant-test",
			"Quantity":   999,
			"DataTypeID": itemDataTypeID,
		}, check.ExpectError)
	case "inboundShipmentNotification":
		execErr(te, ctx, updateNotification, map[string]any{
			"ID":         targetID,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-update check", check.Entity)
	}
}

func checkCrossDelete(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	targetID := mustResolve(t, ids, check.Target)

	switch check.Entity {
	case "inbound":
		execErr(te, ctx, deleteInbound, map[string]any{"ID": targetID}, check.ExpectError)
	case "inboundItem":
		execErr(te, ctx, deleteItem, map[string]any{"ID": targetID}, check.ExpectError)
	case "inboundShipmentNotification":
		execErr(te, ctx, deleteNotification, map[string]any{"ID": targetID}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-delete check", check.Entity)
	}
}

func checkCrossCreate(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()

	switch check.Entity {
	case "inbound":
		execErr(te, ctx, createInboundWithSupplier, map[string]any{
			"SupplierID": mustResolve(t, ids, mustArg(t, check.Args, "supplierID")),
			"DataTypeID": itemDataTypeID,
		}, check.ExpectError)
	case "inboundItem":
		execErr(te, ctx, createItem, map[string]any{
			"InboundID":  mustResolve(t, ids, mustArg(t, check.Args, "inbound")),
			"Sku":        mustArg(t, check.Args, "sku"),
			"Quantity":   mustArgInt(t, check.Args, "quantity"),
			"DataTypeID": itemDataTypeID,
		}, check.ExpectError)
	case "inboundShipmentNotification":
		execErr(te, ctx, createNotification, map[string]any{
			"InboundID":  mustResolve(t, ids, mustArg(t, check.Args, "inbound")),
			"DataTypeID": itemDataTypeID,
		}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-create check", check.Entity)
	}
}

func checkCrossUpdateWithArgs(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	targetID := mustResolve(t, ids, check.Target)

	switch check.Entity {
	case "inbound":
		execErr(te, ctx, updateInboundWithSupplier, map[string]any{
			"ID":         targetID,
			"SupplierID": mustResolve(t, ids, mustArg(t, check.Args, "supplierID")),
		}, check.ExpectError)
	case "inboundItem":
		execErr(te, ctx, updateItemWithInboundID, map[string]any{
			"ID":        targetID,
			"InboundID": mustResolve(t, ids, mustArg(t, check.Args, "inboundID")),
		}, check.ExpectError)
	case "inboundShipmentNotification":
		execErr(te, ctx, updateNotificationWithInboundID, map[string]any{
			"ID":        targetID,
			"InboundID": mustResolve(t, ids, mustArg(t, check.Args, "inboundID")),
		}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-update-with-args check", check.Entity)
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

func mustArg(t *testing.T, args map[string]any, key string) string {
	t.Helper()
	v, ok := args[key]
	require.True(t, ok, "missing arg %q", key)
	s, ok := v.(string)
	require.True(t, ok, "arg %q must be string, got %T", key, v)
	return s
}

func mustArgInt(t *testing.T, args map[string]any, key string) int {
	t.Helper()
	v, ok := args[key]
	require.True(t, ok, "missing arg %q", key)
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		t.Fatalf("arg %q must be number, got %T", key, v)
		return 0
	}
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
