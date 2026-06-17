package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gqlgo/gqlgenc/clientv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
	"github.com/pyck-ai/pyck/backend/common/validator"

	"github.com/pyck-ai/pyck/backend/inventory/api"
	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/enttest"
	entreplenishmentorder "github.com/pyck-ai/pyck/backend/inventory/ent/gen/replenishmentorder"
	entreplenishmentorderitem "github.com/pyck-ai/pyck/backend/inventory/ent/gen/replenishmentorderitem"
	"github.com/pyck-ai/pyck/backend/inventory/model"
	"github.com/pyck-ai/pyck/backend/inventory/resolvers"
	"github.com/pyck-ai/pyck/backend/inventory/service/stock"
)

var (
	testTenantID = uuid.MustParse("b98b88eb-ce77-4e9a-a224-d37443a9c5c1")

	testUser = &authn.User{
		ID:       uuid.MustParse("fdd880fd-c97e-4b8a-83fa-653b1960d87b"),
		TenantID: testTenantID,
		Roles: map[uuid.UUID]authn.Role{
			testTenantID: authn.ROLE_ADMIN,
		},
	}
)

// setupTestServer creates a GraphQL server with enttest database for testing
func setupTestServer(t *testing.T) (*httptest.Server, *ent.Client, context.Context, *mocks.MockPublisher, *mocks.MockDataTypeProvider) {
	t.Helper()

	// Create test database
	dbURI := testresolver.DatabaseURI(t)
	entClient := enttest.Open(t, dialect.SQLite, dbURI, enttest.WithOptions(ent.Log(t.Log))).Debug()

	// Create context with user for privacy checks
	ctx := context.Background()
	ctx = request.Context(ctx, testUser, testUser.TenantID)

	// Set up resolver dependencies
	publisher := new(mocks.MockPublisher)
	inventoryStock, _ := stock.New(dialect.SQLite, nil)
	dataTypeProvider := new(mocks.MockDataTypeProvider)
	validator := validator.NewValidator(dataTypeProvider)

	// Create resolver and schema
	resolver := resolvers.NewResolver(
		"inventory",
		entClient,
		validator,
		inventoryStock,
	)
	schema := resolvers.NewSchema(resolver)

	// Create GraphQL server
	gqlServer := handler.NewDefaultServer(schema)
	gqlServer.Use(gqltx.NewMiddleware(entClient, ent.NewTxContext, "inventory-test", 0))

	// Set up HTTP router with auth middleware
	httpAuth := new(mocks.MockAuthProvider)
	httpAuth.On("HTTPMiddleware").Return(mocks.HTTPMiddleware(testUser)).Maybe()

	httpRouter := chi.NewRouter()
	httpRouter.Use(
		httpAuth.HTTPMiddleware(),
		tenant.HTTPMiddleware(),
	)
	// Mount GraphQL handler at root for gqlgenc client
	httpRouter.Handle("/", gqlServer)

	// Create test server
	server := httptest.NewServer(httpRouter)

	return server, entClient, ctx, publisher, dataTypeProvider
}

func TestReplenishmentOrderCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		orderInput    model.CreateReplenishmentOrderWithItemsInput
		expectedSku   string
		checkDataType bool
		checkData     bool
		itemCount     int
	}{
		{
			name: "minimal fields",
			orderInput: model.CreateReplenishmentOrderWithItemsInput{
				SupplierID: func() *uuid.UUID { id := uuidgql.GenerateV7UUID(); return &id }(),
				Items: []*model.CreateReplenishmentOrderItemsInput{
					{
						Sku:      "CREATE-MINIMAL-SKU",
						Quantity: 10,
					},
				},
			},
			expectedSku: "CREATE-MINIMAL-SKU",
			itemCount:   1,
		},
		{
			name: "with data type",
			orderInput: model.CreateReplenishmentOrderWithItemsInput{
				SupplierID: func() *uuid.UUID { id := uuidgql.GenerateV7UUID(); return &id }(),
				Items: []*model.CreateReplenishmentOrderItemsInput{
					{
						Sku:          "CREATE-WITH-DATATYPE-SKU",
						Quantity:     5,
						DataTypeSlug: func() *string { s := "test-data-type"; return &s }(),
						Data: map[string]any{
							"weight": 5.0,
							"color":  "red",
						},
					},
				},
			},
			expectedSku:   "CREATE-WITH-DATATYPE-SKU",
			checkDataType: true,
			itemCount:     1,
		},
		{
			name: "with custom data",
			orderInput: model.CreateReplenishmentOrderWithItemsInput{
				SupplierID: func() *uuid.UUID { id := uuidgql.GenerateV7UUID(); return &id }(),
				Items: []*model.CreateReplenishmentOrderItemsInput{
					{
						Sku:          "CREATE-WITH-DATA-SKU",
						Quantity:     15,
						DataTypeSlug: func() *string { s := "test-data-type"; return &s }(),
						Data: map[string]any{
							"weight": 10.5,
							"color":  "blue",
						},
					},
				},
			},
			expectedSku: "CREATE-WITH-DATA-SKU",
			checkData:   true,
			itemCount:   1,
		},
		{
			name: "multiple items",
			orderInput: model.CreateReplenishmentOrderWithItemsInput{
				SupplierID: func() *uuid.UUID { id := uuidgql.GenerateV7UUID(); return &id }(),
				Items: []*model.CreateReplenishmentOrderItemsInput{
					{
						Sku:      "ITEM-1-SKU",
						Quantity: 10,
					},
					{
						Sku:      "ITEM-2-SKU",
						Quantity: 20,
					},
				},
			},
			expectedSku: "ITEM-1-SKU",
			itemCount:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, entClient, ctx, publisher, dataTypeProvider := setupTestServer(t)
			defer server.Close()
			defer entClient.Close()

			// Add data type for tests that need it
			if tt.checkDataType || tt.checkData {
				dataTypeProvider.AddDataType(json_schema.DataType{
					ID:   uuidgql.GenerateV7UUID(),
					Slug: "test-data-type",
					JsonSchema: `{
						"type": "object",
						"properties": {
							"weight": {"type": "number"},
							"color": {"type": "string"}
						},
						"additionalProperties": false
					}`,
				})
			}

			// Setup mocks
			publisher.On("SendMutationEventWithReply", mock.Anything).Return([]byte(nil), nil).Maybe()

			// Create API client
			apiClient := api.NewClient(http.DefaultClient, server.URL, &clientv2.Options{
				ParseDataAlongWithErrors: true,
			})

			// Execute create
			result, err := apiClient.CreateReplenishmentOrder(ctx, api.CreateReplenishmentOrderArgs{
				Input: tt.orderInput,
			})
			require.NoError(t, err)
			require.NotNil(t, result)

			// Validate results
			createResult := result.GetCreateReplenishmentOrder()
			require.NotNil(t, createResult)
			createdOrder := createResult.GetReplenishmentOrder()
			require.NotNil(t, createdOrder)

			// Check basic fields
			assert.NotEmpty(t, createdOrder.GetID())
			assert.Equal(t, testTenantID, *createdOrder.GetTenantID())
			assert.Equal(t, *tt.orderInput.SupplierID, *createdOrder.GetSupplierID())

			// Verify order exists in database
			orderID, err := uuid.Parse(createdOrder.GetID())
			require.NoError(t, err)
			dbOrder, err := entClient.ReplenishmentOrder.Get(ctx, orderID)
			require.NoError(t, err)
			assert.Equal(t, *tt.orderInput.SupplierID, dbOrder.SupplierID)

			// Verify items were created
			items, err := dbOrder.QueryReplenishmentOrderItems().AllPages(ctx, mixin.Limit)
			require.NoError(t, err)
			assert.Len(t, items, tt.itemCount)

			// Check first item details
			firstItem := items[0]
			assert.Equal(t, tt.expectedSku, firstItem.Sku)

			// Check data type if set
			if tt.checkDataType {
				assert.NotNil(t, firstItem.DataTypeID)
				assert.Equal(t, "test-data-type", firstItem.DataTypeSlug)
			}

			// Check custom data if set
			if tt.checkData {
				require.NotEmpty(t, firstItem.Data)
				assert.InDelta(t, 10.5, firstItem.Data["weight"], 0.001)
				assert.Equal(t, "blue", firstItem.Data["color"])
			}
		})
	}
}

func TestReplenishmentOrderGet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		orderCount      int
		filterSupplier  bool
		queryArgs       api.GetReplenishmentOrdersArgs
		expectedCount   int
		expectedEdges   int
		checkPagination bool
	}{
		{
			name:       "list all orders",
			orderCount: 2,
			queryArgs: func() api.GetReplenishmentOrdersArgs {
				first := 10
				return api.GetReplenishmentOrdersArgs{First: &first}
			}(),
			expectedCount: 2,
			expectedEdges: 2,
		},
		{
			name:           "filter by supplier",
			orderCount:     3,
			filterSupplier: true,
			expectedCount:  2,
			expectedEdges:  2,
		},
		{
			name:       "pagination",
			orderCount: 5,
			queryArgs: func() api.GetReplenishmentOrdersArgs {
				first := 2
				return api.GetReplenishmentOrdersArgs{First: &first}
			}(),
			expectedCount:   5,
			expectedEdges:   2,
			checkPagination: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, entClient, ctx, _, _ := setupTestServer(t)
			defer server.Close()
			defer entClient.Close()

			// Create test data
			supplier1 := uuidgql.GenerateV7UUID()
			supplier2 := uuidgql.GenerateV7UUID()

			for i := range tt.orderCount {
				supplierID := supplier1
				if tt.filterSupplier && i == tt.orderCount-1 {
					supplierID = supplier2
				}
				entClient.ReplenishmentOrder.Create().
					SetTenantID(testTenantID).
					SetSupplierID(supplierID).
					SaveX(ctx)
			}

			// Create API client
			apiClient := api.NewClient(http.DefaultClient, server.URL, &clientv2.Options{
				ParseDataAlongWithErrors: true,
			})

			// Build query args
			queryArgs := tt.queryArgs
			if tt.filterSupplier {
				queryArgs = api.GetReplenishmentOrdersArgs{
					Where: &api.ReplenishmentOrderWhereInput{
						SupplierID: &supplier1,
					},
				}
			}

			// Execute query
			result, err := apiClient.GetReplenishmentOrders(ctx, queryArgs)
			require.NoError(t, err)
			require.NotNil(t, result)

			// Validate results
			orders := result.GetReplenishmentOrders()
			assert.Equal(t, tt.expectedCount, orders.GetTotalCount())
			assert.Len(t, orders.GetEdges(), tt.expectedEdges)

			// Check pagination if needed
			if tt.checkPagination {
				assert.True(t, orders.GetPageInfo().GetHasNextPage())

				// Get second page
				cursor := orders.GetPageInfo().GetEndCursor()
				first := 2
				result2, err := apiClient.GetReplenishmentOrders(ctx, api.GetReplenishmentOrdersArgs{
					First: &first,
					After: cursor,
				})
				require.NoError(t, err)
				require.NotNil(t, result2)

				orders2 := result2.GetReplenishmentOrders()
				assert.Len(t, orders2.GetEdges(), 2)
			}
		})
	}
}

func TestReplenishmentOrderUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		updateInput   api.UpdateReplenishmentOrderInput
		checkSupplier bool
		checkDataType bool
	}{
		{
			name: "update supplier",
			updateInput: func() api.UpdateReplenishmentOrderInput {
				supplierID := uuidgql.GenerateV7UUID()
				return api.UpdateReplenishmentOrderInput{SupplierID: &supplierID}
			}(),
			checkSupplier: true,
		},
		{
			name: "update with data type",
			updateInput: func() api.UpdateReplenishmentOrderInput {
				dataTypeID := uuidgql.GenerateV7UUID()
				dataTypeSlug := "test-data-type"
				return api.UpdateReplenishmentOrderInput{
					DataTypeID:   &dataTypeID,
					DataTypeSlug: &dataTypeSlug,
				}
			}(),
			checkDataType: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, entClient, ctx, publisher, _ := setupTestServer(t)
			defer server.Close()
			defer entClient.Close()

			// Setup mocks
			publisher.On("SendMutationEventWithReply", mock.Anything).Return([]byte(nil), nil).Maybe()
			publisher.On("SendUpdateEvent", mock.Anything).Return(nil).Maybe()

			// Create test order with items
			order := entClient.ReplenishmentOrder.Create().
				SetTenantID(testTenantID).
				SetSupplierID(uuidgql.GenerateV7UUID()).
				SaveX(ctx)

			// Create related item
			entClient.ReplenishmentOrderItem.Create().
				SetTenantID(testTenantID).
				SetSku("ORIGINAL-ITEM-SKU").
				SetQuantity(10).
				SetReplenishmentOrder(order).
				SaveX(ctx)

			// Create API client
			apiClient := api.NewClient(http.DefaultClient, server.URL, &clientv2.Options{
				ParseDataAlongWithErrors: true,
			})

			// Execute update
			result, err := apiClient.UpdateReplenishmentOrder(ctx, api.UpdateReplenishmentOrderArgs{
				Id:    order.ID.String(),
				Input: tt.updateInput,
			})
			require.NoError(t, err)
			require.NotNil(t, result)

			// Validate results
			updateResult := result.GetUpdateReplenishmentOrder()
			updatedOrder := updateResult.GetReplenishmentOrder()
			assert.NotEmpty(t, updatedOrder.GetID())

			if tt.checkSupplier && tt.updateInput.SupplierID != nil {
				assert.Equal(t, *tt.updateInput.SupplierID, *updatedOrder.GetSupplierID())
			}

			if tt.checkDataType {
				assert.NotNil(t, updatedOrder.GetDataTypeID())
				assert.Equal(t, "test-data-type", *updatedOrder.GetDataTypeSlug())
			}

			// Verify order exists in database with relations intact
			dbOrder, err := entClient.ReplenishmentOrder.Get(ctx, order.ID)
			require.NoError(t, err)

			// Verify items still exist
			items, err := dbOrder.QueryReplenishmentOrderItems().AllPages(ctx, mixin.Limit)
			require.NoError(t, err)
			assert.Len(t, items, 1)
		})
	}
}

func TestReplenishmentOrderDelete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		preDeleted    bool
		nonExistent   bool
		expectSuccess bool
	}{
		{
			name:          "soft delete",
			expectSuccess: true,
		},
		{
			name:          "delete already deleted item",
			preDeleted:    true,
			expectSuccess: false,
		},
		{
			name:          "delete non-existent item",
			nonExistent:   true,
			expectSuccess: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, entClient, ctx, publisher, _ := setupTestServer(t)
			defer server.Close()
			defer entClient.Close()

			// Setup mocks
			publisher.On("SendMutationEventWithReply", mock.Anything).Return([]byte(nil), nil).Maybe()

			var orderID uuid.UUID
			if tt.nonExistent {
				// Use a non-existent UUID
				orderID = uuidgql.GenerateV7UUID()
			} else {
				// Create test order with items
				order := entClient.ReplenishmentOrder.Create().
					SetTenantID(testTenantID).
					SetSupplierID(uuidgql.GenerateV7UUID()).
					SaveX(ctx)
				orderID = order.ID

				// Create related items
				entClient.ReplenishmentOrderItem.Create().
					SetTenantID(testTenantID).
					SetSku("ORDER-ITEM-SKU").
					SetQuantity(10).
					SetReplenishmentOrder(order).
					SaveX(ctx)

				// Pre-delete if needed
				if tt.preDeleted {
					now := time.Now()
					entClient.ReplenishmentOrder.UpdateOneID(orderID).
						SetDeletedAt(now).
						SetDeletedBy(testUser.ID).
						SaveX(ctx)
				}
			}

			// Create API client
			apiClient := api.NewClient(http.DefaultClient, server.URL, &clientv2.Options{
				ParseDataAlongWithErrors: true,
			})

			// Execute delete
			result, err := apiClient.DeleteReplenishmentOrder(ctx, api.DeleteReplenishmentOrderArgs{
				Id: orderID.String(),
			})

			if tt.expectSuccess {
				require.NoError(t, err)
				require.NotNil(t, result)

				// Verify soft delete - order should still exist in DB with deleted_at set
				ctxWithDeleted := feature.Context(ctx, feature.FEATURE_SHOW_DELETED)
				dbOrder, err := entClient.ReplenishmentOrder.Query().
					Where(entreplenishmentorder.ID(orderID)).
					Only(ctxWithDeleted)
				require.NoError(t, err, "Order should still exist in database")
				require.NotNil(t, dbOrder.DeletedAt, "Order should have deleted_at timestamp set")
				require.Equal(t, testUser.ID, dbOrder.DeletedBy, "Order should have deleted_by set to current user")

				// Verify order is not found via normal query (without WithDeleted)
				_, err = entClient.ReplenishmentOrder.Get(ctx, orderID)
				require.Error(t, err, "Order should not be found via normal query after soft delete")
				require.True(t, ent.IsNotFound(err), "Error should be NotFound error")

				// Verify related items still exist (they should be soft deleted as well through cascading)
				items, err := entClient.ReplenishmentOrderItem.Query().
					Where(entreplenishmentorderitem.ReplenishmentOrderID(orderID)).
					AllPages(ctxWithDeleted, mixin.Limit)
				require.NoError(t, err)
				assert.Len(t, items, 1)
			} else {
				require.Error(t, err)
				// Note: result may not be nil even on error, just verify error occurred
			}
		})
	}
}
