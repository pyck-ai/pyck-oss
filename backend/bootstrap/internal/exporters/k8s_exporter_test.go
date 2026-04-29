package exporters

import (
	"context"
	"fmt"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockK8sSecretClient is a mock implementation of K8sSecretClient
type MockK8sSecretClient struct {
	mock.Mock
}

func (m *MockK8sSecretClient) SecretExists(ctx context.Context, name string, key string) (bool, error) {
	args := m.Called(ctx, name, key)
	return args.Bool(0), args.Error(1)
}

func (m *MockK8sSecretClient) UpsertSecrets(ctx context.Context, name string, data map[string][]byte) error {
	args := m.Called(ctx, name, data)
	return args.Error(0)
}

// Verify MockK8sSecretClient satisfies both interfaces
var _ K8sSecretClient = (*MockK8sSecretClient)(nil)

// TestK8sExporterWithClient tests successful export with a mock client
func TestK8sExporterWithClient(t *testing.T) {
	mockClient := new(MockK8sSecretClient)
	namespace := "test-namespace"

	ctx := context.Background()
	credentials := "test-secret-token-12345"
	export := Export{
		Type: ExportTypeK8s,
		Name: "test-key",
	}

	// Expect UpsertSecrets to be called with specific parameters
	expectedData := map[string][]byte{
		"test-key": []byte(credentials),
	}
	mockClient.On("UpsertSecrets", ctx, "pyck-secrets", expectedData).Return(nil)

	exporter := NewK8sExporterWithClient(namespace, "pyck-secrets", mockClient)
	err := exporter.Export(ctx, credentials, export)

	require.NoError(t, err, "Export should succeed")
	mockClient.AssertExpectations(t)
	mockClient.AssertCalled(t, "UpsertSecrets", ctx, "pyck-secrets", expectedData)
}

// TestK8sExporterClientError tests error handling when client operations fail
func TestK8sExporterClientError(t *testing.T) {
	mockClient := new(MockK8sSecretClient)
	namespace := "test-namespace"

	ctx := context.Background()
	credentials := "test-secret-token"
	export := Export{
		Type: ExportTypeK8s,
		Name: "test-key",
	}

	// Mock client returns an error
	expectedData := map[string][]byte{
		"test-key": []byte(credentials),
	}
	mockClient.On("UpsertSecrets", ctx, "pyck-secrets", expectedData).Return(fmt.Errorf("connection refused"))

	exporter := NewK8sExporterWithClient(namespace, "pyck-secrets", mockClient)
	err := exporter.Export(ctx, credentials, export)

	require.Error(t, err, "Export should fail with client error")
	require.Contains(t, err.Error(), "failed to save credentials")
}

// TestK8sExporterNoNamespace tests behavior when namespace is not configured
func TestK8sExporterNoNamespace(t *testing.T) {
	mockClient := new(MockK8sSecretClient)
	namespace := "" // Empty namespace

	ctx := context.Background()
	credentials := "test-secret-token"
	export := Export{
		Type: ExportTypeK8s,
		Name: "test-key",
	}

	exporter := NewK8sExporterWithClient(namespace, "pyck-secrets", mockClient)
	err := exporter.Export(ctx, credentials, export)

	// Should succeed without error when namespace is empty (graceful skip)
	require.NoError(t, err, "Export should gracefully skip when namespace is empty")
	// Client should not be called
	mockClient.AssertNotCalled(t, "UpsertSecrets")
}

// TestK8sExporterNilClient tests behavior when client is nil
func TestK8sExporterNilClient(t *testing.T) {
	namespace := "test-namespace"

	ctx := context.Background()
	credentials := "test-secret-token"
	export := Export{
		Type: ExportTypeK8s,
		Name: "test-key",
	}

	exporter := NewK8sExporterWithClient(namespace, "pyck-secrets", nil)
	err := exporter.Export(ctx, credentials, export)

	require.Error(t, err, "Export should fail when client is nil")
	require.Contains(t, err.Error(), "Kubernetes client not initialized")
}

// TestK8sExporterMultipleExports tests exporting multiple credentials to the same secret
func TestK8sExporterMultipleExports(t *testing.T) {
	mockClient := new(MockK8sSecretClient)
	namespace := "test-namespace"

	ctx := context.Background()

	// First export
	export1 := Export{
		Type: ExportTypeK8s,
		Name: "api-token",
	}
	expectedData1 := map[string][]byte{
		"api-token": []byte("token-123"),
	}
	mockClient.On("UpsertSecrets", ctx, "pyck-secrets", expectedData1).Return(nil)

	exporter := NewK8sExporterWithClient(namespace, "pyck-secrets", mockClient)
	err := exporter.Export(ctx, "token-123", export1)
	require.NoError(t, err, "First export should succeed")

	// Second export with different key
	export2 := Export{
		Type: ExportTypeK8s,
		Name: "worker-token",
	}
	expectedData2 := map[string][]byte{
		"worker-token": []byte("token-456"),
	}
	mockClient.On("UpsertSecrets", ctx, "pyck-secrets", expectedData2).Return(nil)

	err = exporter.Export(ctx, "token-456", export2)
	require.NoError(t, err, "Second export should succeed")

	// Verify both calls were made
	mockClient.AssertNumberOfCalls(t, "UpsertSecrets", 2)
	mockClient.AssertExpectations(t)
}

// TestK8sExporterSpecialCharactersInCredentials tests that special characters are preserved
func TestK8sExporterSpecialCharactersInCredentials(t *testing.T) {
	mockClient := new(MockK8sSecretClient)
	namespace := "test-namespace"

	ctx := context.Background()

	// Credentials with special characters (JWT-like)
	credentials := "eyJhbGciOiJFUzI1NiIsImtpZCI6IjEifQ.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ"
	export := Export{
		Type: ExportTypeK8s,
		Name: "jwt-token",
	}

	expectedData := map[string][]byte{
		"jwt-token": []byte(credentials),
	}
	mockClient.On("UpsertSecrets", ctx, "pyck-secrets", expectedData).Return(nil)

	exporter := NewK8sExporterWithClient(namespace, "pyck-secrets", mockClient)
	err := exporter.Export(ctx, credentials, export)

	require.NoError(t, err, "Export should succeed with special characters")
	mockClient.AssertCalled(t, "UpsertSecrets", ctx, "pyck-secrets", expectedData)
}

// TestK8sExporterWithContextLogger tests that logger is extracted from context
func TestK8sExporterWithContextLogger(t *testing.T) {
	mockClient := new(MockK8sSecretClient)
	namespace := "test-namespace"

	// Create context with logger
	ctx := log.Context(context.Background(), log.DefaultLogger())

	credentials := "test-secret-value"
	export := Export{
		Type: ExportTypeK8s,
		Name: "test-key",
	}

	expectedData := map[string][]byte{
		"test-key": []byte(credentials),
	}
	mockClient.On("UpsertSecrets", ctx, "pyck-secrets", expectedData).Return(nil)

	exporter := NewK8sExporterWithClient(namespace, "pyck-secrets", mockClient)
	err := exporter.Export(ctx, credentials, export)

	require.NoError(t, err, "Export with context logger should succeed")
	mockClient.AssertExpectations(t)
}

// TestK8sExporterSecretName tests that the fixed secret name is used
func TestK8sExporterSecretName(t *testing.T) {
	mockClient := new(MockK8sSecretClient)
	namespace := "test-namespace"

	ctx := context.Background()
	credentials := "token-value"
	export := Export{
		Type: ExportTypeK8s,
		Name: "my-key",
	}

	// Verify the exact secret name used
	var capturedSecretName string
	mockClient.On("UpsertSecrets", mock.MatchedBy(func(c context.Context) bool {
		return c == ctx
	}), mock.MatchedBy(func(name string) bool {
		capturedSecretName = name
		return name == "pyck-secrets"
	}), mock.Anything).Return(nil)

	exporter := NewK8sExporterWithClient(namespace, "pyck-secrets", mockClient)
	err := exporter.Export(ctx, credentials, export)

	require.NoError(t, err, "Export should succeed")
	require.Equal(t, "pyck-secrets", capturedSecretName, "Secret name should always be 'pyck-secrets'")
}

// TestK8sExporterEmptyCredentials tests exporting empty credentials
func TestK8sExporterEmptyCredentials(t *testing.T) {
	mockClient := new(MockK8sSecretClient)
	namespace := "test-namespace"

	ctx := context.Background()
	export := Export{
		Type: ExportTypeK8s,
		Name: "empty-key",
	}

	expectedData := map[string][]byte{
		"empty-key": []byte(""),
	}
	mockClient.On("UpsertSecrets", ctx, "pyck-secrets", expectedData).Return(nil)

	exporter := NewK8sExporterWithClient(namespace, "pyck-secrets", mockClient)
	err := exporter.Export(ctx, "", export)

	require.NoError(t, err, "Export should succeed even with empty credentials")
	mockClient.AssertCalled(t, "UpsertSecrets", ctx, "pyck-secrets", expectedData)
}

// TestK8sExporterLargeCredentials tests exporting large credentials
func TestK8sExporterLargeCredentials(t *testing.T) {
	mockClient := new(MockK8sSecretClient)
	namespace := "test-namespace"

	ctx := context.Background()

	// Create a large credential string (simulate a JSON keyfile)
	largeCredentials := ""
	for i := 0; i < 1000; i++ {
		largeCredentials += "x"
	}

	export := Export{
		Type: ExportTypeK8s,
		Name: "large-key",
	}

	expectedData := map[string][]byte{
		"large-key": []byte(largeCredentials),
	}
	mockClient.On("UpsertSecrets", ctx, "pyck-secrets", expectedData).Return(nil)

	exporter := NewK8sExporterWithClient(namespace, "pyck-secrets", mockClient)
	err := exporter.Export(ctx, largeCredentials, export)

	require.NoError(t, err, "Export should succeed with large credentials")
	mockClient.AssertCalled(t, "UpsertSecrets", ctx, "pyck-secrets", expectedData)
}

// TestK8sExporterCustomSecretName tests using a custom secret name
func TestK8sExporterCustomSecretName(t *testing.T) {
	mockClient := new(MockK8sSecretClient)
	namespace := "production-namespace"
	customSecretName := "pyck-tenant-secrets"

	ctx := context.Background()
	credentials := "tenant-token-789"
	export := Export{
		Type: ExportTypeK8s,
		Name: "tenant-service-token",
	}

	// Verify custom secret name is used
	expectedData := map[string][]byte{
		"tenant-service-token": []byte(credentials),
	}
	mockClient.On("UpsertSecrets", ctx, customSecretName, expectedData).Return(nil)

	exporter := NewK8sExporterWithClient(namespace, customSecretName, mockClient)
	err := exporter.Export(ctx, credentials, export)

	require.NoError(t, err, "Export should succeed with custom secret name")
	mockClient.AssertCalled(t, "UpsertSecrets", ctx, customSecretName, expectedData)
	mockClient.AssertNotCalled(t, "UpsertSecrets", ctx, "pyck-secrets", mock.Anything)
}
