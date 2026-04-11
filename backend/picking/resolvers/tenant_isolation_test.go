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

var updateOrderItemWithOrderID = testresolver.ParseTemplate(`mutation {
	updatePickingOrderItem(id: "{{.ID}}", input: {
		orderID: "{{.OrderID}}"
	}) {
		pickingOrderItem { id tenantID orderID }
	}
}`)

var updateNotificationWithOrderID = testresolver.ParseTemplate(`mutation {
	updatePickingOutboundShipmentNotification(id: "{{.ID}}", input: {
		orderID: "{{.OrderID}}"
	}) {
		pickingOutboundShipmentNotification { id tenantID orderID }
	}
}`)

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
		Tenants                       []tenantDef                        `yaml:"tenants"`
		Orders                        []orderSeed                        `yaml:"orders"`
		OrderItems                    []orderItemSeed                    `yaml:"orderItems"`
		OutboundShipmentNotifications []outboundShipmentNotificationSeed `yaml:"outboundShipmentNotifications"`
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
	orderSeed struct {
		Name   string `yaml:"name"`
		Tenant string `yaml:"tenant"`
	}

	orderItemSeed struct {
		Name     string `yaml:"name"`
		Tenant   string `yaml:"tenant"`
		Order    string `yaml:"order"`
		Sku      string `yaml:"sku"`
		Quantity int64  `yaml:"quantity"`
	}

	outboundShipmentNotificationSeed struct {
		Name   string `yaml:"name"`
		Tenant string `yaml:"tenant"`
		Order  string `yaml:"order"`
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
			seedOrders(t, te, scenario.Seed.Orders, ids)
			seedOrderItems(t, te, scenario.Seed.OrderItems, ids)
			seedOutboundShipmentNotifications(t, te, scenario.Seed.OutboundShipmentNotifications, ids)

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

func seedOrders(t *testing.T, te *testEnv, orders []orderSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, os := range orders {
		user := mustTenantUser(t, os.Tenant)
		key := os.Tenant + "/" + os.Name
		ids[key] = te.newOrder(te.ctx(user), user).Create().ID
		t.Logf("Seeded order: %s (id=%s)", key, ids[key])
	}
}

func seedOutboundShipmentNotifications(t *testing.T, te *testEnv, notifications []outboundShipmentNotificationSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, ns := range notifications {
		user := mustTenantUser(t, ns.Tenant)
		orderID := mustResolve(t, ids, ns.Tenant+"/"+ns.Order)
		notif := te.newNotification(te.ctx(user), user, orderID).Create()

		key := ns.Tenant + "/" + ns.Name
		ids[key] = notif.ID
		t.Logf("Seeded outbound shipment notification: %s (id=%s)", key, ids[key])
	}
}

func seedOrderItems(t *testing.T, te *testEnv, items []orderItemSeed, ids map[string]uuid.UUID) {
	t.Helper()
	for _, is := range items {
		user := mustTenantUser(t, is.Tenant)
		orderID := mustResolve(t, ids, is.Tenant+"/"+is.Order)
		item := te.newOrderItem(te.ctx(user), user, orderID).
			Sku(is.Sku).
			Quantity(is.Quantity).
			Create()

		key := is.Tenant + "/" + is.Name
		ids[key] = item.ID
		t.Logf("Seeded order item: %s (id=%s)", key, ids[key])
	}
}

// =============================================================================
// CHECK HANDLERS
// =============================================================================

func checkQuery(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck) {
	t.Helper()

	switch check.Entity {
	case "order":
		data := execOK[queryOrdersData](te, ctx, queryOrders, nil)
		assert.Equal(t, check.ExpectCount, data.PickingOrders.TotalCount)
	case "orderItem":
		data := execOK[queryOrderItemsData](te, ctx, queryOrderItems, nil)
		assert.Equal(t, check.ExpectCount, data.PickingOrderItems.TotalCount)
	case "outboundShipmentNotification":
		data := execOK[queryNotificationsData](te, ctx, queryNotifications, nil)
		assert.Equal(t, check.ExpectCount, data.PickingOutboundShipmentNotifications.TotalCount)
	default:
		t.Fatalf("unsupported entity %q for query check", check.Entity)
	}
}

func checkCrossUpdate(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()
	targetID := mustResolve(t, ids, check.Target)

	switch check.Entity {
	case "order":
		execErr(te, ctx, updateOrder, map[string]any{
			"ID":         targetID,
			"CustomerID": uuid.New(),
			"DataTypeID": itemDataTypeID,
		}, check.ExpectError)
	case "orderItem":
		execErr(te, ctx, updateOrderItem, map[string]any{
			"ID":         targetID,
			"Sku":        "cross-tenant-test",
			"Quantity":   999,
			"DataTypeID": itemDataTypeID,
		}, check.ExpectError)
	case "outboundShipmentNotification":
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
	case "order":
		execErr(te, ctx, deleteOrder, map[string]any{"ID": targetID}, check.ExpectError)
	case "orderItem":
		execErr(te, ctx, deleteOrderItem, map[string]any{"ID": targetID}, check.ExpectError)
	case "outboundShipmentNotification":
		execErr(te, ctx, deleteNotification, map[string]any{"ID": targetID}, check.ExpectError)
	default:
		t.Fatalf("unsupported entity %q for cross-delete check", check.Entity)
	}
}

func checkCrossCreate(t *testing.T, te *testEnv, ctx context.Context, check tenantCheck, ids map[string]uuid.UUID) {
	t.Helper()

	switch check.Entity {
	case "order":
		execErr(te, ctx, createOrder, map[string]any{
			"CustomerID": mustResolve(t, ids, mustArg(t, check.Args, "customerID")),
			"DataTypeID": itemDataTypeID,
		}, check.ExpectError)
	case "orderItem":
		execErr(te, ctx, createOrderItem, map[string]any{
			"OrderID":    mustResolve(t, ids, mustArg(t, check.Args, "order")),
			"Sku":        mustArg(t, check.Args, "sku"),
			"Quantity":   mustArgInt(t, check.Args, "quantity"),
			"DataTypeID": itemDataTypeID,
		}, check.ExpectError)
	case "outboundShipmentNotification":
		execErr(te, ctx, createNotification, map[string]any{
			"OrderID":    mustResolve(t, ids, mustArg(t, check.Args, "order")),
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
	case "order":
		execErr(te, ctx, updateOrder, map[string]any{
			"ID":         targetID,
			"CustomerID": mustResolve(t, ids, mustArg(t, check.Args, "customerID")),
		}, check.ExpectError)
	case "orderItem":
		execErr(te, ctx, updateOrderItemWithOrderID, map[string]any{
			"ID":      targetID,
			"OrderID": mustResolve(t, ids, mustArg(t, check.Args, "orderID")),
		}, check.ExpectError)
	case "outboundShipmentNotification":
		execErr(te, ctx, updateNotificationWithOrderID, map[string]any{
			"ID":      targetID,
			"OrderID": mustResolve(t, ids, mustArg(t, check.Args, "orderID")),
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
