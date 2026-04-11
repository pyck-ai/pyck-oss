package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/Yamashou/gqlgenc/clientv2"
	"github.com/go-chi/chi"
	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
	"github.com/pyck-ai/pyck/backend/common/validator"

	"github.com/pyck-ai/pyck/backend/file/api"
	ent "github.com/pyck-ai/pyck/backend/file/ent/gen"
	"github.com/pyck-ai/pyck/backend/file/ent/gen/enttest"
	entfile "github.com/pyck-ai/pyck/backend/file/ent/gen/file"
	"github.com/pyck-ai/pyck/backend/file/resolvers"
	"github.com/pyck-ai/pyck/backend/file/services"
)

// mockMinioClient is a mock implementation of MinIO client for testing
type mockMinioClient struct{}

func (m *mockMinioClient) BucketExists(ctx context.Context, bucketName string) (bool, error) {
	return true, nil
}

func (m *mockMinioClient) MakeBucket(ctx context.Context, bucketName string, opts minio.MakeBucketOptions) error {
	return nil
}

func (m *mockMinioClient) RemoveObject(ctx context.Context, bucketName, objectName string, opts minio.RemoveObjectOptions) error {
	return nil
}

func (m *mockMinioClient) PresignedPutObject(ctx context.Context, bucketName, objectName string, expires time.Duration) (*url.URL, error) {
	mockURL, _ := url.Parse("https://mocked-s3-url.example.com/" + objectName)
	return mockURL, nil
}

func (m *mockMinioClient) PresignedGetObject(ctx context.Context, bucketName, objectName string, expires time.Duration, reqParams url.Values) (*url.URL, error) {
	mockURL, _ := url.Parse("https://mocked-s3-url.example.com/" + objectName)
	return mockURL, nil
}

// createMockS3Storage creates a mock S3 storage service for testing
func createMockS3Storage() *services.S3StorageService {
	return &services.S3StorageService{
		Bucket:       "test-bucket",
		HTTPScheme:   "https",
		HTTPEndpoint: "mocked-s3-url.example.com",
		MinioClient:  &mockMinioClient{},
	}
}

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
func setupTestServer(t *testing.T) (*httptest.Server, *ent.Client, context.Context) {
	t.Helper()

	// Create test database
	dbURI := testresolver.DatabaseURI(t)
	entClient := enttest.Open(t, dialect.SQLite, dbURI, enttest.WithOptions(ent.Log(t.Log))).Debug()

	// Create context with user for privacy checks
	ctx := context.Background()
	ctx = request.Context(ctx, testUser, testUser.TenantID)

	// Set up resolver dependencies
	dataTypeProvider := new(mocks.MockDataTypeProvider)
	validator := validator.NewValidator(dataTypeProvider)

	// Create mock S3 storage service
	mockS3 := createMockS3Storage()

	// Create resolver and schema
	resolver := resolvers.NewResolver(
		"file",
		entClient,
		validator,
		mockS3,
		nil, // workflowClient - not needed for read operations
	)
	schema := resolvers.NewSchema(resolver)

	// Create GraphQL server
	gqlServer := handler.NewDefaultServer(schema)

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

	return server, entClient, ctx
}

// TestGetFiles tests the GetFiles API client method
func TestGetFiles(t *testing.T) {
	t.Parallel()

	server, entClient, ctx := setupTestServer(t)
	t.Cleanup(func() {
		server.Close()
		entClient.Close()
	})

	refID := uuidgql.GenerateV7UUID()

	// Create test files
	testFile1 := entClient.File.Create().
		SetTenantID(testUser.TenantID).
		SetRefid(refID).
		SetReftype(entfile.ReftypeSupplier).
		SetName("test-file-1.pdf").
		SetSize(1024).
		SetContentType("application/pdf").
		SaveX(ctx)

	testFile2 := entClient.File.Create().
		SetTenantID(testUser.TenantID).
		SetRefid(refID).
		SetReftype(entfile.ReftypeSupplier).
		SetName("test-file-2.txt").
		SetSize(512).
		SetContentType("text/plain").
		SaveX(ctx)

	tests := []struct {
		name         string
		input        api.GetFilesArgs
		expectError  bool
		validateFunc func(t *testing.T, result *api.GetFiles)
	}{
		{
			name: "successful query with pagination",
			input: api.GetFilesArgs{
				First: intPtr(10),
			},
			expectError: false,
			validateFunc: func(t *testing.T, result *api.GetFiles) {
				t.Helper()
				require.NotNil(t, result)
				assert.Equal(t, 2, result.Files.TotalCount)
				assert.Len(t, result.Files.Edges, 2)
				assert.Equal(t, testFile1.ID.String(), result.Files.Edges[0].Node.ID)
				assert.Equal(t, testFile2.ID.String(), result.Files.Edges[1].Node.ID)
			},
		},
		{
			name: "successful query with where filter",
			input: api.GetFilesArgs{
				Where: &api.FileWhereInput{
					Name: stringPtr("test-file-1.pdf"),
				},
			},
			expectError: false,
			validateFunc: func(t *testing.T, result *api.GetFiles) {
				t.Helper()
				require.NotNil(t, result)
				assert.Equal(t, 1, result.Files.TotalCount)
				assert.Len(t, result.Files.Edges, 1)
				assert.Equal(t, "test-file-1.pdf", result.Files.Edges[0].Node.Name)
			},
		},
		{
			name: "empty result set",
			input: api.GetFilesArgs{
				Where: &api.FileWhereInput{
					Name: stringPtr("non-existent-file.pdf"),
				},
			},
			expectError: false,
			validateFunc: func(t *testing.T, result *api.GetFiles) {
				t.Helper()
				require.NotNil(t, result)
				assert.Equal(t, 0, result.Files.TotalCount)
				assert.Empty(t, result.Files.Edges)
			},
		},
	}

	// Create API client
	clientOptions := &clientv2.Options{
		ParseDataAlongWithErrors: true,
	}
	apiClient := api.NewClient(http.DefaultClient, server.URL, clientOptions)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Execute GetFiles
			result, err := apiClient.GetFiles(ctx, tt.input)

			// Validate
			if tt.expectError {
				require.Error(t, err)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, result)
				}
			}
		})
	}
}

// TestGetFilesReturnsAllFields verifies that GetFiles returns all file fields correctly
func TestGetFilesReturnsAllFields(t *testing.T) {
	t.Parallel()

	server, entClient, ctx := setupTestServer(t)
	defer func() {
		server.Close()
		entClient.Close()
	}()

	// Create test file with all fields
	refID := uuidgql.GenerateV7UUID()
	testFile := entClient.File.Create().
		SetTenantID(testUser.TenantID).
		SetRefid(refID).
		SetReftype(entfile.ReftypeSupplier).
		SetName("test-document.pdf").
		SetSize(2048).
		SetContentType("application/pdf").
		SetDescription("Test description").
		SaveX(ctx)

	// Create API client
	clientOptions := &clientv2.Options{
		ParseDataAlongWithErrors: true,
	}
	apiClient := api.NewClient(http.DefaultClient, server.URL, clientOptions)

	// Execute
	result, err := apiClient.GetFiles(ctx, api.GetFilesArgs{})

	// Validate
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Files.Edges, 1)

	node := result.Files.Edges[0].Node
	assert.Equal(t, testFile.ID.String(), node.ID)
	assert.Equal(t, testUser.TenantID, node.TenantID)
	assert.Equal(t, "test-document.pdf", node.Name)
	assert.Equal(t, 2048, node.Size) // GraphQL returns int, not int64
	assert.Equal(t, "application/pdf", node.ContentType)
	assert.Equal(t, "Test description", *node.Description) // Description is now *string
}

// Helper functions

func intPtr(i int) *int {
	return &i
}

func stringPtr(s string) *string {
	return &s
}
