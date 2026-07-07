package resolvers_test

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gqlgo/gqlgenc/clientv2"
	"github.com/riandyrn/otelchi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"

	"github.com/pyck-ai/pyck/backend/inventory/api"
	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
	"github.com/pyck-ai/pyck/backend/inventory/model"
)

//go:embed testdata/stock/*.test.yaml
var testdata embed.FS

// ptr returns a pointer to the given value.
func ptr[T any](v T) *T {
	return &v
}

// =============================================================================
// SHARED GRAPHQL TEMPLATES
// =============================================================================

// stocksQueryTemplate is used by multiple test files to query stock levels.
var stocksQueryTemplate = testresolver.ParseTemplate(`
	query {
		stocks(
			where: {{or .Where "null"}},
			last: {{or .Last "null"}},
		) {
			totalCount
			edges {
				node {
					id
					tenantID
					repositoryID
					itemID
					quantity
					incomingStock
					outgoingStock
					ownQuantity
					ownIncomingStock
					ownOutgoingStock
					createdAt
					createdBy
					updatedAt
					updatedBy
				}
				cursor
			}
			pageInfo {
				hasNextPage
				hasPreviousPage
				startCursor
				endCursor
			}
		}
	}`)

// =============================================================================
// STOCK VALIDATION HELPERS
// =============================================================================

// getStockForRepo queries stocks filtered by repositoryID and itemID, returning
// the first matching stock node or nil if no stock exists for that combination.
func getStockForRepo(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	repoID string,
	itemID string,
) *api.GetStocks_Stocks_Edges_Node {
	t.Helper()

	result, err := apiClient.GetStocks(ctx, api.GetStocksArgs{
		Where: &api.StockWhereInput{
			RepositoryID: &repoID,
			ItemID:       &itemID,
		},
		OrderBy: &api.StockOrder{
			Field:     ptr(api.StockOrderFieldCreatedAt),
			Direction: api.OrderDirectionDesc,
		},
		First: ptr(1),
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	stocks := result.GetStocks()
	require.NotNil(t, stocks)

	edges := stocks.GetEdges()
	if len(edges) == 0 {
		return nil
	}

	return edges[0].GetNode()
}

// stockTestCreateRepository is a helper to create a repository via the API client.
func stockTestCreateRepository(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	name string,
	repoType entrepository.Type,
	virtual bool,
	parentID *string,
) string {
	t.Helper()

	result, err := apiClient.CreateInventoryRepository(ctx, api.CreateInventoryRepositoryArgs{
		Input: api.CreateRepositoryInput{
			Name:        name,
			Type:        repoType,
			VirtualRepo: &virtual,
			ParentID:    parentID,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	created := result.GetCreateInventoryRepository()
	require.NotNil(t, created)

	repo := created.GetInventoryRepository()
	require.NotNil(t, repo)
	assert.NotEmpty(t, repo.GetID())
	assert.Equal(t, name, repo.GetName())

	return repo.GetID()
}

// stockTestCreateItem is a helper to create an item via the API client.
func stockTestCreateItem(t *testing.T, ctx context.Context, apiClient api.Client, sku string) string {
	t.Helper()

	result, err := apiClient.CreateInventoryItem(ctx, api.CreateInventoryItemArgs{
		Input: api.CreateInventoryItemInput{
			Sku: sku,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	created := result.GetCreateInventoryItem()
	require.NotNil(t, created)

	item := created.GetInventoryItem()
	require.NotNil(t, item)
	assert.NotEmpty(t, item.GetID())
	assert.Equal(t, sku, item.GetSku())

	return item.GetID()
}

// stockTestCreateItemMovement is a helper to create an item movement via the API client.
func stockTestCreateItemMovement(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	itemID string,
	fromID string,
	toID string,
	quantity int,
) string {
	t.Helper()

	result, err := apiClient.CreateInventoryItemMovement(ctx, api.CreateInventoryItemMovementArgs{
		Input: api.CreateItemMovementInput{
			Quantity: quantity,
			Handler:  testHandler,
			FromID:   fromID,
			ToID:     toID,
			ItemID:   itemID,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	created := result.GetCreateInventoryItemMovement()
	require.NotNil(t, created)

	movement := created.GetInventoryItemMovement()
	require.NotNil(t, movement)
	assert.NotEmpty(t, movement.GetID())
	assert.Equal(t, quantity, movement.GetQuantity())

	return movement.GetID()
}

// stockTestExecuteItemMovement is a helper to execute an item movement via the API client.
func stockTestExecuteItemMovement(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	movementID string,
) {
	t.Helper()

	result, err := apiClient.ExecuteInventoryItemMovement(ctx, api.ExecuteInventoryItemMovementArgs{
		Id: movementID,
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	executed := result.GetExecuteInventoryItemMovement()
	require.NotNil(t, executed)

	movement := executed.GetInventoryItemMovement()
	require.NotNil(t, movement)
	assert.True(t, movement.GetExecuted())
}

// stockTestCreateCollectionMovement is a helper to create a collection movement via the API client.
// It creates multiple movements in a single API call and returns the ID of the first movement.
func stockTestCreateCollectionMovement(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	itemID string,
	fromID string,
	toID string,
	quantity int,
) string {
	t.Helper()

	handler := testHandler
	qty := float64(quantity)

	// Parse UUIDs from strings
	parsedItemID, err := uuid.Parse(itemID)
	require.NoError(t, err, "failed to parse itemID")

	parsedFromID, err := uuid.Parse(fromID)
	require.NoError(t, err, "failed to parse fromID")

	parsedToID, err := uuid.Parse(toID)
	require.NoError(t, err, "failed to parse toID")

	result, err := apiClient.CreateInventoryCollectionMovement(ctx, api.CreateInventoryCollectionMovementArgs{
		Input: model.CreateCollectionMovementInput{
			Handler: &handler,
			Collection: []*model.CollectionMovementArrayInput{
				{
					Handler:  handler,
					FromID:   parsedFromID,
					ToID:     parsedToID,
					ItemID:   &parsedItemID,
					Quantity: &qty,
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	created := result.GetCreateInventoryCollectionMovement()
	require.NotNil(t, created)

	movements := created.GetMovements()
	require.NotEmpty(t, movements, "collection movement must create at least one movement")

	movement := movements[0]
	require.NotNil(t, movement)
	assert.NotEmpty(t, movement.GetID())

	return movement.GetID()
}

// stockTestCreateRepositoryMovement is a helper to create a repository movement via the API client.
func stockTestCreateRepositoryMovement(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	repositoryID string,
	fromID string,
	toID string,
) string {
	t.Helper()

	result, err := apiClient.CreateInventoryRepositoryMovement(ctx, api.CreateInventoryRepositoryMovementArgs{
		Input: api.CreateRepositoryMovementInput{
			Handler:      testHandler,
			FromID:       &fromID,
			ToID:         toID,
			RepositoryID: repositoryID,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	created := result.GetCreateInventoryRepositoryMovement()
	require.NotNil(t, created)

	movement := created.GetInventoryRepositoryMovement()
	require.NotNil(t, movement)
	assert.NotEmpty(t, movement.GetID())
	assert.Equal(t, repositoryID, movement.GetRepositoryID())

	return movement.GetID()
}

// stockTestExecuteRepositoryMovement is a helper to execute a repository movement via the API client.
func stockTestExecuteRepositoryMovement(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	movementID string,
) {
	t.Helper()

	result, err := apiClient.ExecuteInventoryRepositoryMovement(ctx, api.ExecuteInventoryRepositoryMovementArgs{
		Id: movementID,
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	executed := result.GetExecuteInventoryRepositoryMovement()
	require.NotNil(t, executed)

	movement := executed.GetInventoryRepositoryMovement()
	require.NotNil(t, movement)
	assert.True(t, movement.GetExecuted())
}

// stockTestDeleteItemMovement is a helper to delete (soft-delete) an item movement via the API client.
func stockTestDeleteItemMovement(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	movementID string,
) {
	t.Helper()

	result, err := apiClient.DeleteInventoryItemMovement(ctx, api.DeleteInventoryItemMovementArgs{
		Id: movementID,
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	deleted := result.GetDeleteInventoryItemMovement()
	require.NotNil(t, deleted)
	assert.NotNil(t, deleted.GetDeletedID())
}

// stockTestDeleteRepositoryMovement is a helper to delete (soft-delete) a repository movement via the API client.
func stockTestDeleteRepositoryMovement(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	movementID string,
) {
	t.Helper()

	result, err := apiClient.DeleteInventoryRepositoryMovement(ctx, api.DeleteInventoryRepositoryMovementArgs{
		Id: movementID,
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	deleted := result.GetDeleteInventoryRepositoryMovement()
	require.NotNil(t, deleted)
	assert.NotNil(t, deleted.GetDeletedID())
}

// stockTestTryExecuteItemMovement attempts to execute an item movement and returns the error (if any).
func stockTestTryExecuteItemMovement(
	ctx context.Context,
	apiClient api.Client,
	movementID string,
) error {
	_, err := apiClient.ExecuteInventoryItemMovement(ctx, api.ExecuteInventoryItemMovementArgs{
		Id: movementID,
	})
	return err
}

// stockTestTryExecuteRepositoryMovement attempts to execute a repository movement and returns the error (if any).
func stockTestTryExecuteRepositoryMovement(
	ctx context.Context,
	apiClient api.Client,
	movementID string,
) error {
	_, err := apiClient.ExecuteInventoryRepositoryMovement(ctx, api.ExecuteInventoryRepositoryMovementArgs{
		Id: movementID,
	})
	return err
}

// stockTestTryDeleteItemMovement attempts to delete an item movement and returns the error (if any).
func stockTestTryDeleteItemMovement(
	ctx context.Context,
	apiClient api.Client,
	movementID string,
) error {
	_, err := apiClient.DeleteInventoryItemMovement(ctx, api.DeleteInventoryItemMovementArgs{
		Id: movementID,
	})
	return err
}

// stockTestTryDeleteRepositoryMovement attempts to delete a repository movement and returns the error (if any).
func stockTestTryDeleteRepositoryMovement(
	ctx context.Context,
	apiClient api.Client,
	movementID string,
) error {
	_, err := apiClient.DeleteInventoryRepositoryMovement(ctx, api.DeleteInventoryRepositoryMovementArgs{
		Id: movementID,
	})
	return err
}

// stockTestTryCreateItemMovement attempts to create an item movement and returns the error (if any).
func stockTestTryCreateItemMovement(
	ctx context.Context,
	apiClient api.Client,
	itemID string,
	fromID string,
	toID string,
	quantity int,
) error {
	_, err := apiClient.CreateInventoryItemMovement(ctx, api.CreateInventoryItemMovementArgs{
		Input: api.CreateItemMovementInput{
			Quantity: quantity,
			Handler:  testHandler,
			FromID:   fromID,
			ToID:     toID,
			ItemID:   itemID,
		},
	})
	return err
}

// stockTestTryCreateItemMovementWithID attempts to create an item movement and returns
// the new movement's ID and any error. On success: (id, nil). On failure: ("", err).
// This is the preferred helper when the caller wants to attempt a movement and
// gracefully handle failures (e.g. insufficient stock) without failing the test.
func stockTestTryCreateItemMovementWithID(
	ctx context.Context,
	apiClient api.Client,
	itemID string,
	fromID string,
	toID string,
	quantity int,
) (string, error) {
	result, err := apiClient.CreateInventoryItemMovement(ctx, api.CreateInventoryItemMovementArgs{
		Input: api.CreateItemMovementInput{
			Quantity: quantity,
			Handler:  testHandler,
			FromID:   fromID,
			ToID:     toID,
			ItemID:   itemID,
		},
	})
	if err != nil {
		return "", err
	}

	created := result.GetCreateInventoryItemMovement()
	if created == nil {
		return "", nil
	}

	movement := created.GetInventoryItemMovement()
	if movement == nil {
		return "", nil
	}

	return movement.GetID(), nil
}

// stockTestTryCreateRepositoryMovement attempts to create a repository movement and returns the error (if any).
func stockTestTryCreateRepositoryMovement(
	ctx context.Context,
	apiClient api.Client,
	repositoryID string,
	fromID string,
	toID string,
) error {
	_, err := apiClient.CreateInventoryRepositoryMovement(ctx, api.CreateInventoryRepositoryMovementArgs{
		Input: api.CreateRepositoryMovementInput{
			Handler:      testHandler,
			FromID:       &fromID,
			ToID:         toID,
			RepositoryID: repositoryID,
		},
	})
	return err
}

// handleMovementCreateWithError handles movement creation when an error is expected.
func handleMovementCreateWithError(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	stepFrom string,
	stepTo string,
	stepMoveType string,
	stepName string,
	stepExpectError string,
	stepQty int,
	repoMap map[string]string,
	parentTracker map[string]string,
	itemID string,
) {
	t.Helper()

	fromID := repoMap[stepFrom]
	toID := repoMap[stepTo]
	require.NotEmpty(t, fromID, "from repository %q not found", stepFrom)
	require.NotEmpty(t, toID, "to repository %q not found", stepTo)

	var createErr error
	switch stepMoveType {
	case moveTypeItem:
		createErr = stockTestTryCreateItemMovement(ctx, apiClient, itemID, fromID, toID, stepQty)
	case moveTypeRepo:
		repositoryID := fromID
		fromParentID, ok := parentTracker[stepFrom]
		require.True(t, ok, "parent tracker not found for repository %q", stepFrom)
		createErr = stockTestTryCreateRepositoryMovement(ctx, apiClient, repositoryID, fromParentID, toID)
	default:
		t.Fatalf("expectError not supported for movement type: %s", stepMoveType)
	}

	require.Error(t, createErr, "expected create to fail for step %q", stepName)
	assert.Contains(t, createErr.Error(), stepExpectError, "error message mismatch for step %q", stepName)
}

// handleItemMovement creates a single item movement.
func handleItemMovement(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	itemID string,
	stepFrom string,
	stepTo string,
	stepQty int,
	repoMap map[string]string,
) string {
	t.Helper()

	fromID := repoMap[stepFrom]
	toID := repoMap[stepTo]
	require.NotEmpty(t, fromID, "from repository %q not found", stepFrom)
	require.NotEmpty(t, toID, "to repository %q not found", stepTo)
	return stockTestCreateItemMovement(t, ctx, apiClient, itemID, fromID, toID, stepQty)
}

// handleRepositoryMovement creates a repository movement and updates the parent tracker.
func handleRepositoryMovement(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	stepFrom string,
	stepTo string,
	repoMap map[string]string,
	parentTracker map[string]string,
) string {
	t.Helper()

	fromID := repoMap[stepFrom]
	toID := repoMap[stepTo]
	require.NotEmpty(t, fromID, "from repository %q not found", stepFrom)
	require.NotEmpty(t, toID, "to repository %q not found", stepTo)
	repositoryID := fromID

	fromParentID, ok := parentTracker[stepFrom]
	require.True(t, ok, "parent tracker not found for repository %q", stepFrom)

	movementID := stockTestCreateRepositoryMovement(t, ctx, apiClient, repositoryID, fromParentID, toID)

	parentTracker[stepFrom] = toID
	return movementID
}

// handleItemCollectionMovement handles both single-entry and multi-entry collection movements.
func handleItemCollectionMovement(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	step *movementStep,
	itemID string,
	repoMap map[string]string,
	parentTracker map[string]string,
	movements map[string]movementInfo,
) string {
	t.Helper()

	if len(step.Collection) == 0 {
		return handleSingleEntryCollection(t, ctx, apiClient, itemID, step.From, step.To, step.Qty, repoMap)
	}

	return handleMultiEntryCollection(t, ctx, apiClient, step, itemID, repoMap, parentTracker, movements)
}

// handleSingleEntryCollection creates a single-entry collection movement.
func handleSingleEntryCollection(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	itemID string,
	stepFrom string,
	stepTo string,
	stepQty int,
	repoMap map[string]string,
) string {
	t.Helper()

	fromID := repoMap[stepFrom]
	toID := repoMap[stepTo]
	require.NotEmpty(t, fromID, "from repository %q not found", stepFrom)
	require.NotEmpty(t, toID, "to repository %q not found", stepTo)
	return stockTestCreateCollectionMovement(t, ctx, apiClient, itemID, fromID, toID, stepQty)
}

// handleMultiEntryCollection creates a multi-entry collection movement with support for mixed types.
func handleMultiEntryCollection(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	step *movementStep,
	itemID string,
	repoMap map[string]string,
	parentTracker map[string]string,
	movements map[string]movementInfo,
) string {
	t.Helper()

	handler := testHandler
	parsedItemID, err := uuid.Parse(itemID)
	require.NoError(t, err)

	collectionInputs := buildCollectionInputs(t, step, parsedItemID, handler, repoMap, parentTracker)

	result, err := apiClient.CreateInventoryCollectionMovement(ctx, api.CreateInventoryCollectionMovementArgs{
		Input: model.CreateCollectionMovementInput{
			Handler:    &handler,
			Collection: collectionInputs,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	storeCollectionMovements(t, result, step, movements)
	return ""
}

// buildCollectionInputs builds the collection movement inputs from step entries.
func buildCollectionInputs(
	t *testing.T,
	step *movementStep,
	parsedItemID uuid.UUID,
	handler string,
	repoMap map[string]string,
	parentTracker map[string]string,
) []*model.CollectionMovementArrayInput {
	t.Helper()

	collectionInputs := make([]*model.CollectionMovementArrayInput, len(step.Collection))
	for i, entry := range step.Collection {
		entryFromID, err := uuid.Parse(repoMap[entry.From])
		require.NoError(t, err)
		entryToID, err := uuid.Parse(repoMap[entry.To])
		require.NoError(t, err)

		entryMoveType := entry.MoveType
		if entryMoveType == "" {
			entryMoveType = moveTypeItem
		}

		if entryMoveType == moveTypeRepo {
			collectionInputs[i] = buildRepositoryCollectionInput(t, entry, entryFromID, entryToID, handler, parentTracker)
		} else {
			collectionInputs[i] = buildItemCollectionInput(entry, entryFromID, entryToID, parsedItemID, handler)
		}
	}
	return collectionInputs
}

// buildRepositoryCollectionInput builds a repository movement input for collection.
func buildRepositoryCollectionInput(
	t *testing.T,
	entry collectionEntry,
	entryFromID uuid.UUID,
	entryToID uuid.UUID,
	handler string,
	parentTracker map[string]string,
) *model.CollectionMovementArrayInput {
	t.Helper()

	fromParentID, ok := parentTracker[entry.From]
	require.True(t, ok, "parent tracker not found for repository %q", entry.From)
	parentID, err := uuid.Parse(fromParentID)
	require.NoError(t, err)

	return &model.CollectionMovementArrayInput{
		Handler:      handler,
		FromID:       parentID,
		ToID:         entryToID,
		RepositoryID: &entryFromID,
	}
}

// buildItemCollectionInput builds an item movement input for collection.
func buildItemCollectionInput(
	entry collectionEntry,
	entryFromID uuid.UUID,
	entryToID uuid.UUID,
	parsedItemID uuid.UUID,
	handler string,
) *model.CollectionMovementArrayInput {
	qty := float64(entry.Qty)
	return &model.CollectionMovementArrayInput{
		Handler:  handler,
		FromID:   entryFromID,
		ToID:     entryToID,
		ItemID:   &parsedItemID,
		Quantity: &qty,
	}
}

// storeCollectionMovements stores movement IDs from a collection creation result.
func storeCollectionMovements(
	t *testing.T,
	result *api.CreateInventoryCollectionMovement,
	step *movementStep,
	movements map[string]movementInfo,
) {
	t.Helper()

	created := result.GetCreateInventoryCollectionMovement()
	require.NotNil(t, created)
	mvs := created.GetMovements()
	require.Len(t, mvs, len(step.Collection), "collection must create %d movements", len(step.Collection))

	for i, mv := range mvs {
		key := fmt.Sprintf("%s[%d]", step.Name, i)
		mt := moveTypeItemCollection
		if step.Collection[i].MoveType == moveTypeRepo {
			mt = moveTypeRepo
		}
		movements[key] = movementInfo{id: mv.GetID(), moveType: mt}
	}
}

// setupAPIClient creates an httptest.Server from the test environment's GraphQL
// server and returns an api.Client configured to talk to it. The server is
// cleaned up automatically when the test finishes.
func setupAPIClient(t *testing.T, te *testEnv) api.Client {
	t.Helper()

	return setupAPIClientForUser(t, te, userA)
}

// setupAPIClientForUser creates an httptest.Server using the given user for
// authentication and returns an api.Client configured to talk to it. The server
// is cleaned up automatically when the test finishes.
func setupAPIClientForUser(t *testing.T, te *testEnv, user *authn.User) api.Client {
	t.Helper()

	httpAuth := new(mocks.MockAuthProvider)
	httpAuth.On("HTTPMiddleware").Return(mocks.HTTPMiddleware(user)).Maybe()

	httpRouter := chi.NewRouter()
	httpRouter.Use(
		otelchi.Middleware("test-resolver"),
		httpAuth.HTTPMiddleware(),
		tenant.HTTPMiddleware(),
		feature.HTTPMiddleware(),
	)
	httpRouter.Handle("/", te.GQLServer)

	server := httptest.NewServer(httpRouter)
	t.Cleanup(server.Close)

	return api.NewClient(http.DefaultClient, server.URL, &clientv2.Options{
		ParseDataAlongWithErrors: false,
	})
}

// =============================================================================
// TABLE-DRIVEN TEST DATA STRUCTURES
// =============================================================================

const (
	actionMovementCreate  = "create"  // Create a new movement
	actionMovementExecute = "execute" // Execute an existing movement
	actionMovementDelete  = "delete"  // Delete (soft-delete) an existing movement

	moveTypeItem           = "item"       // Individual item movement
	moveTypeItemCollection = "collection" // Collection of item movements (batch operation)
	moveTypeRepo           = "repository" // Repository movement (relocating an entire repository)
)

// movementInfo tracks movement IDs and types by step name for delete/execute actions.
type movementInfo struct {
	id       string
	moveType string
}

// collectionEntry represents a single movement within a multi-entry collection.
// It can represent either an item movement (from/to repositories with quantity) or
// a repository movement (parent-child repository relationship).
//
// For item movements:
//   - MoveType: "item" (or omitted for backward compatibility)
//   - From: source repository name
//   - To: destination repository name
//   - Qty: quantity to move
//
// For repository movements:
//   - MoveType: "repository"
//   - From: repository to be moved (child)
//   - To: new parent repository
//   - Qty: not used (can be omitted or 0)
//
// Example YAML for mixed collection:
//
//	collection:
//	  - from: storage
//	    to: staging
//	    qty: 10
//	    movetype: item
//	  - from: room1
//	    to: building
//	    moveType: repository
type collectionEntry struct {
	From     string `yaml:"from"`     // Source repository name (for items) or repository to move (for repos)
	To       string `yaml:"to"`       // Destination repository name (for items) or new parent (for repos)
	Qty      int    `yaml:"qty"`      // Quantity to move (only used for item movements)
	MoveType string `yaml:"moveType"` // Movement type: "item" (default) or "repository"
}

// stockLevel represents expected stock levels for a repository at a specific point in time.
type stockLevel struct {
	Quantity         int `yaml:"qty"`    // Total quantity (own + aggregated from children)
	OwnQuantity      int `yaml:"ownQty"` // Own quantity (directly in this repository)
	IncomingStock    int `yaml:"in"`     // Total incoming stock (pending movements into this repo)
	OwnIncomingStock int `yaml:"ownIn"`  // Own incoming stock
	OutgoingStock    int `yaml:"out"`    // Total outgoing stock (pending movements out of this repo)
	OwnOutgoingStock int `yaml:"ownOut"` // Own outgoing stock
}

// repoInit defines a repository to create with its configuration.
type repoInit struct {
	Name     string `yaml:"name"`    // Repository name (used as key in maps)
	Parent   string `yaml:"parent"`  // Parent repository name (empty string for root repositories)
	RepoType string `yaml:"type"`    // Repository type string: "static" or "dynamic"
	Virtual  bool   `yaml:"virtual"` // Whether this is a virtual repository
}

// entType converts the string RepoType to entrepository.Type enum.
func (r repoInit) entType() entrepository.Type {
	if r.RepoType == "dynamic" {
		return entrepository.TypeDynamic
	}
	return entrepository.TypeStatic
}

// movementStep describes a single movement in the test sequence.
type movementStep struct {
	// Movement description
	Name        string            `yaml:"name"`        // Step name for test output (e.g., "create_item_virtual_to_shelf")
	Action      string            `yaml:"action"`      // "create" or "execute"
	MoveType    string            `yaml:"moveType"`    // "item" (individual), "collection" (single or multi-entry), or "repository"
	From        string            `yaml:"from"`        // Source repository name
	To          string            `yaml:"to"`          // Destination repository name
	Qty         int               `yaml:"qty"`         // Quantity for item movements (ignored for repository movements)
	Movement    string            `yaml:"movement"`    // For "delete" or "execute" actions: reference to the step name that created the movement
	ExpectError string            `yaml:"expectError"` // If set, the action is expected to fail with an error containing this substring
	Collection  []collectionEntry `yaml:"collection"`  // For multi-entry collection: list of movements to create
	Item        string            `yaml:"item"`        // Item SKU for multi-item scenarios (defaults to first/only item)

	// Expected stock levels AFTER this step completes
	// Map of repository name → expected stock levels
	// nil stockLevel means: don't validate this repository at this step
	// missing entry means: repository should have zero stock (no record)
	ExpectedStocks map[string]*stockLevel `yaml:"expectedStocks"` // Expected stock levels after this step

	// Per-item expected stock levels for multi-item scenarios.
	// Map of item SKU → repository name → expected stock levels.
	// When present, this is used INSTEAD of expectedStocks.
	ExpectedItemStocks map[string]map[string]*stockLevel `yaml:"expectedItemStocks"`
}

// movementFlow defines a complete test scenario with all movements and expected stock levels.
type movementFlow struct {
	Name         string         `yaml:"name"`         // Test name
	ItemSKU      string         `yaml:"itemSKU"`      // Item SKU to use (single-item, backward compat)
	Items        []string       `yaml:"items"`        // List of item SKUs (multi-item; mutually exclusive with itemSKU)
	Repositories []repoInit     `yaml:"repositories"` // Repositories to create (in order)
	Steps        []movementStep `yaml:"steps"`        // Sequence of movements and validations
}

// loadScenarios loads all YAML scenario files from testdata directory
func loadScenarios(t *testing.T) []movementFlow {
	t.Helper()

	entries, err := testdata.ReadDir("testdata/stock")
	require.NoError(t, err, "failed to read testdata/stock directory")

	// Count YAML files for pre-allocation
	yamlCount := 0
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".yaml" {
			yamlCount++
		}
	}

	scenarios := make([]movementFlow, 0, yamlCount)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}

		data, err := testdata.ReadFile(filepath.Join("testdata/stock", entry.Name()))
		require.NoError(t, err, "failed to read file %s", entry.Name())

		var scenario movementFlow
		err = yaml.Unmarshal(data, &scenario)
		require.NoError(t, err, "failed to parse YAML file %s", entry.Name())

		scenarios = append(scenarios, scenario)
	}

	require.NotEmpty(t, scenarios, "no test scenarios found in testdata directory")
	return scenarios
}

// =============================================================================
// STOCK ASSERTION AND QUERY HELPERS
// =============================================================================

// assertStockLevel is a helper that queries stock for a repository/item pair and
// asserts the expected stock levels.
func assertStockLevel(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	repoID string,
	itemID string,
	repoLabel string,
	expected stockLevel,
) {
	t.Helper()

	var actual stockLevel
	if stock := getStockForRepo(t, ctx, apiClient, repoID, itemID); stock != nil {
		actual.Quantity = stock.GetQuantity()
		actual.OwnQuantity = stock.GetOwnQuantity()
		actual.IncomingStock = stock.GetIncomingStock()
		actual.OwnIncomingStock = stock.GetOwnIncomingStock()
		actual.OutgoingStock = stock.GetOutgoingStock()
		actual.OwnOutgoingStock = stock.GetOwnOutgoingStock()
	}

	assert.Equal(t, expected, actual, "stock mismatch for %s", repoLabel)
}

// getStockAtTime queries the stocks endpoint with a time filter and returns
// the first stock node for the given repo/item pair, or nil.
func getStockAtTime(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	repoID string,
	itemID string,
	at time.Time,
) *api.GetStocks_Stocks_Edges_Node {
	t.Helper()

	result, err := apiClient.GetStocks(ctx, api.GetStocksArgs{
		Where: &api.StockWhereInput{
			RepositoryID: &repoID,
			ItemID:       &itemID,
			Time:         &at,
		},
		OrderBy: &api.StockOrder{
			Field:     ptr(api.StockOrderFieldCreatedAt),
			Direction: api.OrderDirectionDesc,
		},
		First: ptr(1),
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	edges := result.GetStocks().GetEdges()
	if len(edges) == 0 {
		return nil
	}

	return edges[0].GetNode()
}

// latestCreatedAt returns the maximum created_at across the latest stock
// records for the given item in the specified repositories. This provides a
// deterministic time boundary without relying on time.Sleep.
func latestCreatedAt(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	itemID string,
	repoIDs ...string,
) time.Time {
	t.Helper()

	var latest time.Time
	for _, repoID := range repoIDs {
		s := getStockForRepo(t, ctx, apiClient, repoID, itemID)
		if s != nil {
			if ca := s.GetCreatedAt(); ca != nil && ca.After(latest) {
				latest = *ca
			}
		}
	}

	require.False(t, latest.IsZero(), "expected at least one stock record")

	return latest
}

// =============================================================================
// STOCK TESTS
// =============================================================================

// TestStockPlausibility runs table-driven tests for stock movements with plausibility checks
// against the SQLite-backed Go orchestration path.
//
//nolint:tparallel // Subtests execute sequentially to maintain order of movements and validations
func TestStockPlausibility(t *testing.T) {
	t.Parallel()
	runStockPlausibilityScenarios(t, setup)
}

// TestStockPlausibilityPostgres runs the SAME table-driven stock scenarios against
// an embedded PostgreSQL instance with all SQL migrations applied (including
// inventory.create_item_movement_proc). On this path, CreateInventoryItemMovement
// dispatches through the proc rather than the Go orchestration body, which lets us
// observe SQLite ↔ Postgres behavioral differences and surface proc-only bugs.
//
//nolint:tparallel // Subtests execute sequentially to maintain order of movements and validations
func TestStockPlausibilityPostgres(t *testing.T) {
	t.Parallel()
	pg := startEmbeddedPostgres(t)
	runStockPlausibilityScenarios(t, func(t *testing.T) *testEnv {
		t.Helper()
		return setupPostgres(t, pg)
	})
}

// runStockPlausibilityScenarios is the shared body for the two TestStockPlausibility*
// variants. setupFn is either the SQLite-backed `setup` or the embedded-Postgres
// `setupPostgres` adapter.
func runStockPlausibilityScenarios(t *testing.T, setupFn func(*testing.T) *testEnv) {
	t.Helper()

	scenarios := loadScenarios(t)

	//nolint:paralleltest
	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			env := setupFn(t)
			ctx := env.ctx(userA)
			apiClient := setupAPIClient(t, env)

			// Create all repositories upfront
			repoMap := make(map[string]string)       // repoName -> repoID
			parentTracker := make(map[string]string) // repoName -> current parentID

			for _, repoSpec := range scenario.Repositories {
				var parentIDPtr *string
				if repoSpec.Parent != "" {
					parentID := repoMap[repoSpec.Parent]
					require.NotEmpty(t, parentID, "parent repository %q not found", repoSpec.Parent)
					parentIDPtr = &parentID
					parentTracker[repoSpec.Name] = parentID
				} else {
					parentTracker[repoSpec.Name] = ""
				}

				id := stockTestCreateRepository(t, ctx, apiClient, repoSpec.Name, repoSpec.entType(), repoSpec.Virtual, parentIDPtr)
				repoMap[repoSpec.Name] = id

				t.Logf("Created repository: %s (id=%s, parent=%s, type=%v, virtual=%v)",
					repoSpec.Name, id, repoSpec.Parent, repoSpec.RepoType, repoSpec.Virtual)
			}

			// Create items — support both single itemSKU (backward compat) and items list
			itemMap := make(map[string]string) // sku -> itemID
			var defaultItemID string

			if len(scenario.Items) > 0 {
				for _, sku := range scenario.Items {
					id := stockTestCreateItem(t, ctx, apiClient, sku)
					itemMap[sku] = id
					t.Logf("Created item: sku=%s, id=%s", sku, id)
				}
				defaultItemID = itemMap[scenario.Items[0]]
			} else {
				id := stockTestCreateItem(t, ctx, apiClient, scenario.ItemSKU)
				itemMap[scenario.ItemSKU] = id
				defaultItemID = id
				t.Logf("Created item: sku=%s, id=%s", scenario.ItemSKU, id)
			}

			movements := make(map[string]movementInfo)

			// Execute each step in sequence
			for _, step := range scenario.Steps {
				t.Run(step.Name, func(t *testing.T) {
					// Resolve step item ID
					itemID := defaultItemID
					if step.Item != "" {
						resolved, ok := itemMap[step.Item]
						require.True(t, ok, "item %q not found in item map (available: %v)", step.Item, itemMap)
						itemID = resolved
					}

					// Execute action
					switch step.Action {
					case actionMovementCreate:
						if step.ExpectError != "" {
							handleMovementCreateWithError(t, ctx, apiClient, step.From, step.To, step.MoveType,
								step.Name, step.ExpectError, step.Qty, repoMap, parentTracker, itemID)
						} else {
							var movementID string
							switch step.MoveType {
							case moveTypeItem:
								movementID = handleItemMovement(t, ctx, apiClient, itemID, step.From, step.To, step.Qty, repoMap)
							case moveTypeItemCollection:
								movementID = handleItemCollectionMovement(t, ctx, apiClient, &step, itemID, repoMap, parentTracker, movements)
							case moveTypeRepo:
								movementID = handleRepositoryMovement(t, ctx, apiClient, step.From, step.To, repoMap, parentTracker)
							default:
								t.Fatalf("unknown movement type: %s", step.MoveType)
							}
							// Store movement ID and type for later reference (skip for multi-entry collections)
							if movementID != "" {
								movements[step.Name] = movementInfo{id: movementID, moveType: step.MoveType}
							}
						}

					case actionMovementExecute:
						// Get movement info from referenced step
						require.NotEmpty(t, step.Movement, "movement field is required for execute actions")
						info, ok := movements[step.Movement]
						require.True(t, ok, "cannot execute: referenced movement step %q not found", step.Movement)

						if step.ExpectError != "" {
							// Expect execution to fail
							var execErr error
							switch info.moveType {
							case moveTypeItem, moveTypeItemCollection:
								execErr = stockTestTryExecuteItemMovement(ctx, apiClient, info.id)
							case moveTypeRepo:
								execErr = stockTestTryExecuteRepositoryMovement(ctx, apiClient, info.id)
							default:
								t.Fatalf("unknown movement type: %s", info.moveType)
							}
							require.Error(t, execErr, "expected execute to fail for step %q", step.Name)
							assert.Contains(t, execErr.Error(), step.ExpectError, "error message mismatch for step %q", step.Name)
						} else {
							// Normal execution (expect success)
							switch info.moveType {
							case moveTypeItem, moveTypeItemCollection:
								stockTestExecuteItemMovement(t, ctx, apiClient, info.id)
							case moveTypeRepo:
								stockTestExecuteRepositoryMovement(t, ctx, apiClient, info.id)
							default:
								t.Fatalf("unknown movement type: %s", info.moveType)
							}
						}
					case actionMovementDelete:
						// Get movement info from referenced step
						require.NotEmpty(t, step.Movement, "movement field is required for delete actions")
						info, ok := movements[step.Movement]
						require.True(t, ok, "cannot delete: referenced movement step %q not found", step.Movement)

						if step.ExpectError != "" {
							// Expect deletion to fail
							var deleteErr error
							switch info.moveType {
							case moveTypeItem, moveTypeItemCollection:
								deleteErr = stockTestTryDeleteItemMovement(ctx, apiClient, info.id)
							case moveTypeRepo:
								deleteErr = stockTestTryDeleteRepositoryMovement(ctx, apiClient, info.id)
							default:
								t.Fatalf("unknown movement type: %s", info.moveType)
							}
							require.Error(t, deleteErr, "expected delete to fail for step %q", step.Name)
							assert.Contains(t, deleteErr.Error(), step.ExpectError, "error message mismatch for step %q", step.Name)
						} else {
							// Expect deletion to succeed
							switch info.moveType {
							case moveTypeItem, moveTypeItemCollection:
								stockTestDeleteItemMovement(t, ctx, apiClient, info.id)
							case moveTypeRepo:
								stockTestDeleteRepositoryMovement(t, ctx, apiClient, info.id)
							default:
								t.Fatalf("unknown movement type: %s", info.moveType)
							}
							// Remove from tracking after successful deletion
							delete(movements, step.Movement)
						}
					default:
						t.Fatalf("unknown action: %s", step.Action)
					}

					// Validate stock levels for all repositories with expected values
					for repoName, expectedStockLevels := range step.ExpectedStocks {
						if expectedStockLevels == nil {
							continue // Skip validation for this repository
						}

						repoID := repoMap[repoName]
						require.NotEmpty(t, repoID, "repository %q not found in map", repoName)

						var actualStockLevels stockLevel

						if stock := getStockForRepo(t, ctx, apiClient, repoID, itemID); stock != nil {
							actualStockLevels.Quantity = stock.GetQuantity()
							actualStockLevels.OwnQuantity = stock.GetOwnQuantity()
							actualStockLevels.IncomingStock = stock.GetIncomingStock()
							actualStockLevels.OwnIncomingStock = stock.GetOwnIncomingStock()
							actualStockLevels.OutgoingStock = stock.GetOutgoingStock()
							actualStockLevels.OwnOutgoingStock = stock.GetOwnOutgoingStock()
						}

						require.Equal(t, *expectedStockLevels, actualStockLevels, "stock levels do not match expected values for repository %q at step %q", repoName, step.Name)
					}

					// Validate per-item stock levels (multi-item scenarios)
					for itemSKU, repoStocks := range step.ExpectedItemStocks {
						perItemID, ok := itemMap[itemSKU]
						require.True(t, ok, "item %q not found in item map for expectedItemStocks at step %q", itemSKU, step.Name)

						for repoName, expectedStockLevels := range repoStocks {
							if expectedStockLevels == nil {
								continue
							}

							repoID := repoMap[repoName]
							require.NotEmpty(t, repoID, "repository %q not found in map", repoName)

							var actualStockLevels stockLevel

							if stock := getStockForRepo(t, ctx, apiClient, repoID, perItemID); stock != nil {
								actualStockLevels.Quantity = stock.GetQuantity()
								actualStockLevels.OwnQuantity = stock.GetOwnQuantity()
								actualStockLevels.IncomingStock = stock.GetIncomingStock()
								actualStockLevels.OwnIncomingStock = stock.GetOwnIncomingStock()
								actualStockLevels.OutgoingStock = stock.GetOutgoingStock()
								actualStockLevels.OwnOutgoingStock = stock.GetOwnOutgoingStock()
							}

							require.Equal(t, *expectedStockLevels, actualStockLevels, "stock levels do not match expected values for item %q, repository %q at step %q", itemSKU, repoName, step.Name)
						}
					}
				})
			}
		})
	}
}

// TestStockAtTimeWithCollections verifies that point-in-time stock queries
// return the correct historical stock levels when multiple collection movements
// have been executed at different times.
//
//nolint:tparallel
func TestStockAtTimeWithCollections(t *testing.T) {
	t.Parallel()

	env := setup(t)
	ctx := env.ctx(userA)
	apiClient := setupAPIClient(t, env)

	virtualID := stockTestCreateRepository(t, ctx, apiClient, "virtual", entrepository.TypeStatic, true, nil)
	warehouseID := stockTestCreateRepository(t, ctx, apiClient, "warehouse", entrepository.TypeStatic, false, nil)
	shelfID := stockTestCreateRepository(t, ctx, apiClient, "shelf", entrepository.TypeStatic, false, &warehouseID)
	zoneAID := stockTestCreateRepository(t, ctx, apiClient, "zone-a", entrepository.TypeStatic, false, &warehouseID)

	itemID := stockTestCreateItem(t, ctx, apiClient, "time-coll-item")

	// Seed 100 items.
	seedMvID := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, shelfID, 100)
	stockTestExecuteItemMovement(t, ctx, apiClient, seedMvID)

	// Capture the max created_at across all affected repos as our time boundary.
	// Using actual DB timestamps avoids flaky time.Sleep-based approaches.
	timeAfterSeed := latestCreatedAt(t, ctx, apiClient, itemID, shelfID, virtualID).Add(time.Nanosecond)

	// First collection: move 30 items shelf → zone-a, execute.
	handler := testHandler
	qty := float64(30)
	parsedItemID, _ := uuid.Parse(itemID)
	parsedShelfID, _ := uuid.Parse(shelfID)
	parsedZoneAID, _ := uuid.Parse(zoneAID)

	createResult, err := apiClient.CreateInventoryCollectionMovement(ctx, api.CreateInventoryCollectionMovementArgs{
		Input: model.CreateCollectionMovementInput{
			Handler: &handler,
			Collection: []*model.CollectionMovementArrayInput{
				{Handler: handler, FromID: parsedShelfID, ToID: parsedZoneAID, ItemID: &parsedItemID, Quantity: &qty},
			},
		},
	})
	require.NoError(t, err)
	mvID := createResult.GetCreateInventoryCollectionMovement().GetMovements()[0].GetID()
	stockTestExecuteItemMovement(t, ctx, apiClient, mvID)

	// Capture the max created_at across all affected repos after first collection.
	// A single execute may create stock records at slightly different times.
	timeAfterFirst := latestCreatedAt(t, ctx, apiClient, itemID, shelfID, zoneAID).Add(time.Nanosecond)

	// Second collection: move 20 more items shelf → zone-a, execute.
	qty2 := float64(20)
	createResult2, err := apiClient.CreateInventoryCollectionMovement(ctx, api.CreateInventoryCollectionMovementArgs{
		Input: model.CreateCollectionMovementInput{
			Handler: &handler,
			Collection: []*model.CollectionMovementArrayInput{
				{Handler: handler, FromID: parsedShelfID, ToID: parsedZoneAID, ItemID: &parsedItemID, Quantity: &qty2},
			},
		},
	})
	require.NoError(t, err)
	mvID2 := createResult2.GetCreateInventoryCollectionMovement().GetMovements()[0].GetID()
	stockTestExecuteItemMovement(t, ctx, apiClient, mvID2)

	// Now query at three points in time.
	//nolint:paralleltest
	t.Run("after seed only", func(t *testing.T) {
		// At timeAfterSeed: shelf=100, zone-a=0
		stock := getStockAtTime(t, ctx, apiClient, shelfID, itemID, timeAfterSeed)
		require.NotNil(t, stock)
		assert.Equal(t, 100, stock.GetQuantity())

		stock = getStockAtTime(t, ctx, apiClient, zoneAID, itemID, timeAfterSeed)
		if stock != nil {
			assert.Equal(t, 0, stock.GetQuantity())
		}
	})

	//nolint:paralleltest
	t.Run("after first collection", func(t *testing.T) {
		// At timeAfterFirst: shelf=70, zone-a=30
		stock := getStockAtTime(t, ctx, apiClient, shelfID, itemID, timeAfterFirst)
		require.NotNil(t, stock)
		assert.Equal(t, 70, stock.GetQuantity())

		stock = getStockAtTime(t, ctx, apiClient, zoneAID, itemID, timeAfterFirst)
		require.NotNil(t, stock)
		assert.Equal(t, 30, stock.GetQuantity())
	})

	//nolint:paralleltest
	t.Run("current state", func(t *testing.T) {
		// Current: shelf=50, zone-a=50
		assertStockLevel(t, ctx, apiClient, shelfID, itemID, "shelf",
			stockLevel{Quantity: 50, OwnQuantity: 50})
		assertStockLevel(t, ctx, apiClient, zoneAID, itemID, "zone-a",
			stockLevel{Quantity: 50, OwnQuantity: 50})
	})
}

// TestCollectionMultipleItems verifies a collection that moves different items
// in a single batch, ensuring per-item stock tracking remains correct.
//
//nolint:tparallel
func TestCollectionMultipleItems(t *testing.T) {
	t.Parallel()

	env := setup(t)
	ctx := env.ctx(userA)
	apiClient := setupAPIClient(t, env)

	virtualID := stockTestCreateRepository(t, ctx, apiClient, "virtual", entrepository.TypeStatic, true, nil)
	warehouseID := stockTestCreateRepository(t, ctx, apiClient, "warehouse", entrepository.TypeStatic, false, nil)
	shelfID := stockTestCreateRepository(t, ctx, apiClient, "shelf", entrepository.TypeStatic, false, &warehouseID)
	outboundID := stockTestCreateRepository(t, ctx, apiClient, "outbound", entrepository.TypeStatic, false, &warehouseID)

	item1ID := stockTestCreateItem(t, ctx, apiClient, "multi-item-1")
	item2ID := stockTestCreateItem(t, ctx, apiClient, "multi-item-2")

	// Seed: 100 of item1 and 80 of item2 onto shelf.
	seed1 := stockTestCreateItemMovement(t, ctx, apiClient, item1ID, virtualID, shelfID, 100)
	stockTestExecuteItemMovement(t, ctx, apiClient, seed1)
	seed2 := stockTestCreateItemMovement(t, ctx, apiClient, item2ID, virtualID, shelfID, 80)
	stockTestExecuteItemMovement(t, ctx, apiClient, seed2)

	// Create collection with both items moving to outbound.
	handler := testHandler
	qty1, qty2 := float64(25), float64(15)
	parsedItem1ID, _ := uuid.Parse(item1ID)
	parsedItem2ID, _ := uuid.Parse(item2ID)
	parsedShelfID, _ := uuid.Parse(shelfID)
	parsedOutboundID, _ := uuid.Parse(outboundID)

	createResult, err := apiClient.CreateInventoryCollectionMovement(ctx, api.CreateInventoryCollectionMovementArgs{
		Input: model.CreateCollectionMovementInput{
			Handler: &handler,
			Collection: []*model.CollectionMovementArrayInput{
				{Handler: handler, FromID: parsedShelfID, ToID: parsedOutboundID, ItemID: &parsedItem1ID, Quantity: &qty1},
				{Handler: handler, FromID: parsedShelfID, ToID: parsedOutboundID, ItemID: &parsedItem2ID, Quantity: &qty2},
			},
		},
	})
	require.NoError(t, err)
	movements := createResult.GetCreateInventoryCollectionMovement().GetMovements()
	require.Len(t, movements, 2)

	//nolint:paralleltest
	t.Run("reservations after create", func(t *testing.T) {
		// Shelf: item1 out=25, item2 out=15
		s1 := getStockForRepo(t, ctx, apiClient, shelfID, item1ID)
		require.NotNil(t, s1)
		assert.Equal(t, 100, s1.GetQuantity())
		assert.Equal(t, 25, s1.GetOutgoingStock())

		s2 := getStockForRepo(t, ctx, apiClient, shelfID, item2ID)
		require.NotNil(t, s2)
		assert.Equal(t, 80, s2.GetQuantity())
		assert.Equal(t, 15, s2.GetOutgoingStock())

		// Outbound: item1 in=25, item2 in=15
		o1 := getStockForRepo(t, ctx, apiClient, outboundID, item1ID)
		require.NotNil(t, o1)
		assert.Equal(t, 25, o1.GetIncomingStock())

		o2 := getStockForRepo(t, ctx, apiClient, outboundID, item2ID)
		require.NotNil(t, o2)
		assert.Equal(t, 15, o2.GetIncomingStock())
	})

	// Execute position 0 (item1), then position 1 (item2).
	//nolint:paralleltest
	t.Run("execute both", func(t *testing.T) {
		stockTestExecuteItemMovement(t, ctx, apiClient, movements[0].GetID())
		stockTestExecuteItemMovement(t, ctx, apiClient, movements[1].GetID())

		// Shelf: item1=75, item2=65
		s1 := getStockForRepo(t, ctx, apiClient, shelfID, item1ID)
		require.NotNil(t, s1)
		assert.Equal(t, 75, s1.GetQuantity())
		assert.Equal(t, 0, s1.GetOutgoingStock())

		s2 := getStockForRepo(t, ctx, apiClient, shelfID, item2ID)
		require.NotNil(t, s2)
		assert.Equal(t, 65, s2.GetQuantity())
		assert.Equal(t, 0, s2.GetOutgoingStock())

		// Outbound: item1=25, item2=15
		o1 := getStockForRepo(t, ctx, apiClient, outboundID, item1ID)
		require.NotNil(t, o1)
		assert.Equal(t, 25, o1.GetQuantity())

		o2 := getStockForRepo(t, ctx, apiClient, outboundID, item2ID)
		require.NotNil(t, o2)
		assert.Equal(t, 15, o2.GetQuantity())
	})
}

// TestStockAtTimeAfterDeletion tests that point-in-time stock queries return
// correct historical stock levels even after movements have been deleted.
//
//nolint:tparallel
func TestStockAtTimeAfterDeletion(t *testing.T) {
	t.Parallel()

	env := setup(t)
	ctx := env.ctx(userA)
	apiClient := setupAPIClient(t, env)

	// Setup repositories
	virtualID := stockTestCreateRepository(t, ctx, apiClient, "virtual", entrepository.TypeStatic, true, nil)
	warehouseID := stockTestCreateRepository(t, ctx, apiClient, "warehouse", entrepository.TypeStatic, false, nil)
	shelfID := stockTestCreateRepository(t, ctx, apiClient, "shelf", entrepository.TypeStatic, false, &warehouseID)
	outboundID := stockTestCreateRepository(t, ctx, apiClient, "outbound", entrepository.TypeStatic, false, &warehouseID)

	itemID := stockTestCreateItem(t, ctx, apiClient, "time-deletion-item")

	// Step 1: Seed 100 items to shelf and execute
	seedMvID := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, shelfID, 100)
	stockTestExecuteItemMovement(t, ctx, apiClient, seedMvID)

	// Record time T1 (after seed)
	timeT1 := latestCreatedAt(t, ctx, apiClient, itemID, shelfID).Add(time.Nanosecond)

	// Step 2: Create and execute movement shelf→outbound (30 items)
	mv1ID := stockTestCreateItemMovement(t, ctx, apiClient, itemID, shelfID, outboundID, 30)
	stockTestExecuteItemMovement(t, ctx, apiClient, mv1ID)

	// Record time T2 (after first movement executed)
	timeT2 := latestCreatedAt(t, ctx, apiClient, itemID, shelfID, outboundID).Add(time.Nanosecond)

	// Step 3: Create another movement shelf→outbound (20 items), DON'T execute, then delete it
	mv2ID := stockTestCreateItemMovement(t, ctx, apiClient, itemID, shelfID, outboundID, 20)
	stockTestDeleteItemMovement(t, ctx, apiClient, mv2ID)

	// Verify point-in-time queries
	//nolint:paralleltest
	t.Run("stock at T1 shows 100 on shelf", func(t *testing.T) {
		stock := getStockAtTime(t, ctx, apiClient, shelfID, itemID, timeT1)
		require.NotNil(t, stock)
		assert.Equal(t, 100, stock.GetQuantity(), "shelf should have 100 items at T1")
	})

	//nolint:paralleltest
	t.Run("stock at T2 shows 70 on shelf after first movement", func(t *testing.T) {
		stock := getStockAtTime(t, ctx, apiClient, shelfID, itemID, timeT2)
		require.NotNil(t, stock)
		assert.Equal(t, 70, stock.GetQuantity(), "shelf should have 70 items at T2")
	})

	//nolint:paralleltest
	t.Run("current stock shows 70 on shelf (deleted movement doesn't affect)", func(t *testing.T) {
		stock := getStockForRepo(t, ctx, apiClient, shelfID, itemID)
		require.NotNil(t, stock)
		assert.Equal(t, 70, stock.GetQuantity(), "shelf should have 70 items currently")
		assert.Equal(t, 0, stock.GetOutgoingStock(), "no pending outgoing stock")
	})
}

// countStocksAtTime queries stocks with a time filter and returns the total
// count of stock records matching the given repo/item/time combination. This
// is used to verify that the DistinctOnExists subquery correctly deduplicates
// stock records for a given (repo_id, item_id) pair, returning exactly one
// (the latest before the cutoff).
func countStocksAtTime(
	t *testing.T,
	ctx context.Context,
	apiClient api.Client,
	repoID string,
	itemID string,
	at time.Time,
) int {
	t.Helper()

	result, err := apiClient.GetStocks(ctx, api.GetStocksArgs{
		Where: &api.StockWhereInput{
			RepositoryID: &repoID,
			ItemID:       &itemID,
			Time:         &at,
		},
		First: ptr(100),
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	return result.GetStocks().GetTotalCount()
}

// TestStockAtTimeReturnsLatestBeforeCutoffOnly verifies that the time-based
// stock query returns exactly one stock record per (repo, item) pair — the
// latest before the cutoff — even when multiple stock records exist before and
// after the cutoff.
//
// This validates that latestStockIDs correctly passes CreatedAtLT into the
// DistinctOnExists NOT EXISTS subquery: the subquery must only consider rows
// before the cutoff when checking for newer records, so that post-cutoff rows
// do not invalidate valid "latest before cutoff" candidates.
//
//nolint:tparallel
func TestStockAtTimeReturnsLatestBeforeCutoffOnly(t *testing.T) {
	t.Parallel()

	env := setup(t)
	ctx := env.ctx(userA)
	apiClient := setupAPIClient(t, env)

	// Create repos and item.
	virtualID := stockTestCreateRepository(t, ctx, apiClient, "virtual", entrepository.TypeStatic, true, nil)
	shelfID := stockTestCreateRepository(t, ctx, apiClient, "shelf", entrepository.TypeStatic, false, nil)
	itemID := stockTestCreateItem(t, ctx, apiClient, "cutoff-test-item")

	// ── Movement 1: seed 100 items onto shelf (stock state S1: qty=100) ──
	mv1 := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, shelfID, 100)
	stockTestExecuteItemMovement(t, ctx, apiClient, mv1)

	timeAfterS1 := latestCreatedAt(t, ctx, apiClient, itemID, shelfID, virtualID).Add(time.Nanosecond)

	// ── Movement 2: move 30 from shelf → virtual (stock state S2: qty=70) ──
	mv2 := stockTestCreateItemMovement(t, ctx, apiClient, itemID, shelfID, virtualID, 30)
	stockTestExecuteItemMovement(t, ctx, apiClient, mv2)

	timeAfterS2 := latestCreatedAt(t, ctx, apiClient, itemID, shelfID, virtualID).Add(time.Nanosecond)

	// ── Movement 3: move 20 more from shelf → virtual (stock state S3: qty=50) ──
	mv3 := stockTestCreateItemMovement(t, ctx, apiClient, itemID, shelfID, virtualID, 20)
	stockTestExecuteItemMovement(t, ctx, apiClient, mv3)

	// ── Assertions ──

	// Query at timeAfterS1: only S1 records exist before this cutoff.
	// The query must return exactly 1 stock record for (shelf, item) with qty=100.
	// If the bug alleged in the review existed (intermediate rows leaking),
	// totalCount would be > 1.
	//nolint:paralleltest
	t.Run("cutoff after S1 returns exactly one record with qty 100", func(t *testing.T) {
		count := countStocksAtTime(t, ctx, apiClient, shelfID, itemID, timeAfterS1)
		assert.Equal(t, 1, count, "expected exactly 1 stock record for (shelf, item) at cutoff after S1")

		stock := getStockAtTime(t, ctx, apiClient, shelfID, itemID, timeAfterS1)
		require.NotNil(t, stock)
		assert.Equal(t, 100, stock.GetQuantity(), "shelf should have 100 items at cutoff after S1")
	})

	// Query at timeAfterS2: S1 and S2 records exist before this cutoff.
	// The query must return exactly 1 stock record — the latest (S2) — with qty=70.
	// Without CreatedAtLT in the subquery, S3 (after cutoff) would cause the
	// NOT EXISTS check on S2 to find a newer row and incorrectly exclude S2.
	//nolint:paralleltest
	t.Run("cutoff after S2 returns exactly one record with qty 70", func(t *testing.T) {
		count := countStocksAtTime(t, ctx, apiClient, shelfID, itemID, timeAfterS2)
		assert.Equal(t, 1, count, "expected exactly 1 stock record for (shelf, item) at cutoff after S2")

		stock := getStockAtTime(t, ctx, apiClient, shelfID, itemID, timeAfterS2)
		require.NotNil(t, stock)
		assert.Equal(t, 70, stock.GetQuantity(), "shelf should have 70 items at cutoff after S2")
	})

	// Query at current time (no time filter): must return current state qty=50.
	//nolint:paralleltest
	t.Run("current state returns qty 50", func(t *testing.T) {
		assertStockLevel(t, ctx, apiClient, shelfID, itemID, "shelf",
			stockLevel{Quantity: 50, OwnQuantity: 50})
	})
}

// =============================================================================
// RAW STOCK ROW COUNT HELPERS
// =============================================================================

// countRawStockRows returns the total number of stock rows in the DB for a
// (repository, item) pair, bypassing the DistinctOnExists deduplication used
// by the GraphQL stocks query. This lets tests assert on the append-only ledger
// directly rather than the deduplicated "current view".
func countRawStockRows(t *testing.T, entClient *ent.Client, ctx context.Context, repoID, itemID string) int {
	t.Helper()

	repoUUID, err := uuid.Parse(repoID)
	require.NoError(t, err)

	itemUUID, err := uuid.Parse(itemID)
	require.NoError(t, err)

	count, err := entClient.Stock.Query().
		Where(
			entstock.RepositoryID(repoUUID),
			entstock.ItemID(itemUUID),
			entstock.TenantID(tenantA),
		).
		Count(ctx)
	require.NoError(t, err)

	return count
}

// TestUnexecutedMovementDeletionStockRows verifies that:
//  1. Creating an unexecuted item movement inserts exactly one new stock row
//     per affected (repository, item) pair.
//  2. Deleting that unexecuted movement inserts exactly one additional stock
//     row per affected (repository, item) pair — no more, no less.
//  3. After deletion, quantity and own_quantity are preserved on the latest row
//     and all reservation fields are zero.
//  4. A destination repository with no prior stock receives exactly one
//     all-zero row after deletion (the reservation reversal). This is expected
//     append-only ledger behaviour. Multiple such rows would indicate a bug.
//
// Background: a regression was observed in a test environment where deleting
// unexecuted movements produced multiple unexpected zero rows in the stock
// table for destination repositories (e.g. Outbound Zone, Pick Tray 1).
//
//nolint:tparallel
func TestUnexecutedMovementDeletionStockRows(t *testing.T) {
	t.Parallel()

	env := setup(t)
	ctx := env.ctx(userA)
	apiClient := setupAPIClient(t, env)

	// Repository hierarchy:
	//   virtual (root, virtual)
	//   warehouse (root) ─┬─ storage ─── shelf
	//                     └─ outbound
	virtualID := stockTestCreateRepository(t, ctx, apiClient, "deletion-virtual", entrepository.TypeStatic, true, nil)
	warehouseID := stockTestCreateRepository(t, ctx, apiClient, "deletion-warehouse", entrepository.TypeStatic, false, nil)
	storageID := stockTestCreateRepository(t, ctx, apiClient, "deletion-storage", entrepository.TypeStatic, false, &warehouseID)
	shelfID := stockTestCreateRepository(t, ctx, apiClient, "deletion-shelf", entrepository.TypeStatic, false, &storageID)
	outboundID := stockTestCreateRepository(t, ctx, apiClient, "deletion-outbound", entrepository.TypeStatic, false, &warehouseID)

	itemID := stockTestCreateItem(t, ctx, apiClient, "deletion-row-count-item")

	// Seed: move 100 items virtual → shelf and execute so shelf has real stock.
	seed := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, shelfID, 100)
	stockTestExecuteItemMovement(t, ctx, apiClient, seed)

	// Record raw stock row counts per (repo, item) after seeding (baseline).
	baselineShelf := countRawStockRows(t, env.Ent, ctx, shelfID, itemID)
	baselineStorage := countRawStockRows(t, env.Ent, ctx, storageID, itemID)
	baselineOutbound := countRawStockRows(t, env.Ent, ctx, outboundID, itemID)

	// outbound has never been touched — it must have zero rows at baseline.
	require.Equal(t, 0, baselineOutbound, "outbound must have no stock rows before any movement")

	// ── Create unexecuted movement: 20 items shelf → outbound ──────────────
	mv := stockTestCreateItemMovement(t, ctx, apiClient, itemID, shelfID, outboundID, 20)

	//nolint:paralleltest
	t.Run("create inserts exactly one new row per affected repo", func(t *testing.T) {
		assert.Equal(t, baselineShelf+1, countRawStockRows(t, env.Ent, ctx, shelfID, itemID),
			"shelf: expected exactly 1 new stock row after creating movement")
		assert.Equal(t, baselineStorage+1, countRawStockRows(t, env.Ent, ctx, storageID, itemID),
			"storage: expected exactly 1 new stock row after creating movement")
		assert.Equal(t, baselineOutbound+1, countRawStockRows(t, env.Ent, ctx, outboundID, itemID),
			"outbound: expected exactly 1 new stock row after creating movement")
	})

	//nolint:paralleltest
	t.Run("create sets reservation fields correctly", func(t *testing.T) {
		assertStockLevel(t, ctx, apiClient, shelfID, itemID, "shelf",
			stockLevel{Quantity: 100, OwnQuantity: 100, OutgoingStock: 20, OwnOutgoingStock: 20})
		assertStockLevel(t, ctx, apiClient, storageID, itemID, "storage",
			stockLevel{Quantity: 100, OutgoingStock: 20})
		assertStockLevel(t, ctx, apiClient, outboundID, itemID, "outbound",
			stockLevel{IncomingStock: 20, OwnIncomingStock: 20})
	})

	// ── Delete the unexecuted movement ─────────────────────────────────────
	stockTestDeleteItemMovement(t, ctx, apiClient, mv)

	//nolint:paralleltest
	t.Run("delete inserts exactly one new row per affected repo", func(t *testing.T) {
		assert.Equal(t, baselineShelf+2, countRawStockRows(t, env.Ent, ctx, shelfID, itemID),
			"shelf: expected exactly 1 new stock row after deleting movement")
		assert.Equal(t, baselineStorage+2, countRawStockRows(t, env.Ent, ctx, storageID, itemID),
			"storage: expected exactly 1 new stock row after deleting movement")
		// outbound goes from 0 → 1 (create) → 2 (delete reversal).
		// The second row is an all-zero row — this is the expected reversal record.
		// More than 2 rows would indicate a bug (extra zero rows being inserted).
		assert.Equal(t, baselineOutbound+2, countRawStockRows(t, env.Ent, ctx, outboundID, itemID),
			"outbound: expected exactly 1 new stock row after deleting movement")
	})

	//nolint:paralleltest
	t.Run("delete preserves quantity and clears reservations", func(t *testing.T) {
		assertStockLevel(t, ctx, apiClient, shelfID, itemID, "shelf",
			stockLevel{Quantity: 100, OwnQuantity: 100})
		assertStockLevel(t, ctx, apiClient, storageID, itemID, "storage",
			stockLevel{Quantity: 100})
		// outbound never held real stock: its latest row is all-zero after deletion.
		// This is correct — the reservation is reversed, quantity was never set.
		assertStockLevel(t, ctx, apiClient, outboundID, itemID, "outbound",
			stockLevel{})
	})
}

// =============================================================================
// AllPages PAGINATION TESTS
// =============================================================================

// TestAllPagesPaginatesBeyondLimit verifies that AllPages returns all rows
// even when the total exceeds a single page. A small pageSize is used so
// we don't need hundreds of entities.
//
//nolint:tparallel
func TestAllPagesPaginatesBeyondLimit(t *testing.T) {
	t.Parallel()

	env := setup(t)
	ctx := env.ctx(userA)
	apiClient := setupAPIClient(t, env)

	// Create a repository and item via the API, consistent with other stock tests.
	virtualID := stockTestCreateRepository(t, ctx, apiClient, "allpages-virtual", entrepository.TypeStatic, true, nil)
	shelfID := stockTestCreateRepository(t, ctx, apiClient, "allpages-shelf", entrepository.TypeStatic, false, nil)
	itemID := stockTestCreateItem(t, ctx, apiClient, "allpages-item")

	// Seed stock by creating and executing 13 movements (each adds 1 item).
	const totalMovements = 13
	for i := range totalMovements {
		mvID := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, shelfID, 1)
		stockTestExecuteItemMovement(t, ctx, apiClient, mvID)
		t.Logf("Executed movement %d/%d: %s", i+1, totalMovements, mvID)
	}

	// Each create+execute pair produces stock rows. Count the raw rows for
	// the (shelf, item) pair — this is what AllPages must return in full.
	shelfUUID, _ := uuid.Parse(shelfID)
	itemUUID, _ := uuid.Parse(itemID)

	totalRows, err := env.Ent.Stock.Query().
		Where(entstock.RepositoryID(shelfUUID), entstock.ItemID(itemUUID)).
		Count(ctx)
	require.NoError(t, err)
	require.Greater(t, totalRows, 5, "Need more than 5 stock rows to test pagination")

	//nolint:paralleltest
	t.Run("small page size returns all rows", func(t *testing.T) {
		err := env.withTx(ctx, func(tx *ent.Tx) error {
			all, err := tx.Stock.Query().
				Where(entstock.RepositoryID(shelfUUID), entstock.ItemID(itemUUID)).
				AllPages(ctx, 5)
			require.NoError(t, err)
			assert.Len(t, all, totalRows, "AllPages(5) should return all %d stock rows", totalRows)
			return nil
		})
		require.NoError(t, err)
	})

	//nolint:paralleltest
	t.Run("page size equal to row count returns all rows", func(t *testing.T) {
		err := env.withTx(ctx, func(tx *ent.Tx) error {
			all, err := tx.Stock.Query().
				Where(entstock.RepositoryID(shelfUUID), entstock.ItemID(itemUUID)).
				AllPages(ctx, totalRows)
			require.NoError(t, err)
			assert.Len(t, all, totalRows)
			return nil
		})
		require.NoError(t, err)
	})

	//nolint:paralleltest
	t.Run("page size larger than row count returns all rows", func(t *testing.T) {
		err := env.withTx(ctx, func(tx *ent.Tx) error {
			all, err := tx.Stock.Query().
				Where(entstock.RepositoryID(shelfUUID), entstock.ItemID(itemUUID)).
				AllPages(ctx, 200)
			require.NoError(t, err)
			assert.Len(t, all, totalRows)
			return nil
		})
		require.NoError(t, err)
	})

	//nolint:paralleltest
	t.Run("empty result returns empty slice", func(t *testing.T) {
		fakeID := uuid.New()
		err := env.withTx(ctx, func(tx *ent.Tx) error {
			all, err := tx.Stock.Query().
				Where(entstock.RepositoryID(fakeID)).
				AllPages(ctx, 5)
			require.NoError(t, err)
			assert.Empty(t, all)
			return nil
		})
		require.NoError(t, err)
	})
}
