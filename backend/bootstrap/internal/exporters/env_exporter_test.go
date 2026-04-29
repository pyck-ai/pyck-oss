package exporters

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/stretchr/testify/require"
)

// TestEnvExporterCreateNewFile tests creating a new .env file
func TestEnvExporterCreateNewFile(t *testing.T) {
	tmpDir := t.TempDir()
	envFilePath := filepath.Join(tmpDir, "env-test")
	exporter := NewEnvExporter(tmpDir)
	ctx := context.Background()

	token := "test-token-value"
	export := Export{
		Type: ExportTypeEnv,
		File: "env-test",
		Name: "SERVICE_PYCK_TOKEN",
	}

	err := exporter.Export(ctx, token, export)
	require.NoError(t, err, "Export should succeed")

	// Verify file was created with correct content
	content, err := os.ReadFile(envFilePath)
	require.NoError(t, err, "Failed to read .env file")

	expected := "SERVICE_PYCK_TOKEN=test-token-value"
	require.Equal(t, expected, string(content), "File content should match expected")
}

// TestEnvExporterAppendNewVariable tests appending a new variable to existing file
func TestEnvExporterAppendNewVariable(t *testing.T) {
	tmpDir := t.TempDir()
	envFilePath := filepath.Join(tmpDir, "env-test")

	// Create initial .env file with one variable
	initialContent := "EXISTING_VAR=existing-value"
	err := os.WriteFile(envFilePath, []byte(initialContent), 0600)
	require.NoError(t, err, "Failed to create initial .env")

	exporter := NewEnvExporter(tmpDir)
	ctx := context.Background()

	token := "new-token-value"
	export := Export{
		Type: ExportTypeEnv,
		File: "env-test",
		Name: "SERVICE_PYCK_TOKEN",
	}

	err = exporter.Export(ctx, token, export)
	require.NoError(t, err, "Export should succeed")

	content, err := os.ReadFile(envFilePath)
	require.NoError(t, err, "Failed to read .env file")

	contentStr := string(content)
	require.Contains(t, contentStr, "EXISTING_VAR=existing-value", "Original variable should be preserved")
	require.Contains(t, contentStr, "SERVICE_PYCK_TOKEN=new-token-value", "New variable should be appended")
}

// TestEnvExporterUpdateExistingVariable tests updating an existing variable
func TestEnvExporterUpdateExistingVariable(t *testing.T) {
	tmpDir := t.TempDir()
	envFilePath := filepath.Join(tmpDir, "env-test")

	// Create initial .env file with existing variable
	initialContent := "SERVICE_PYCK_TOKEN=old-token-value\nOTHER_VAR=other-value"
	err := os.WriteFile(envFilePath, []byte(initialContent), 0600)
	require.NoError(t, err, "Failed to create initial .env")

	exporter := NewEnvExporter(tmpDir)
	ctx := context.Background()

	newToken := "new-token-value"
	export := Export{
		Type: ExportTypeEnv,
		File: "env-test",
		Name: "SERVICE_PYCK_TOKEN",
	}

	err = exporter.Export(ctx, newToken, export)
	require.NoError(t, err, "Export should succeed")

	content, err := os.ReadFile(envFilePath)
	require.NoError(t, err, "Failed to read .env file")

	contentStr := string(content)
	lines := strings.Split(contentStr, "\n")

	// Verify the variable was updated, not duplicated
	tokenVarCount := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "SERVICE_PYCK_TOKEN=") {
			tokenVarCount++
			require.True(t, strings.HasSuffix(line, "new-token-value"), "Variable should be updated")
		}
	}

	require.Equal(t, 1, tokenVarCount, "Should have exactly 1 SERVICE_PYCK_TOKEN entry")
	require.Contains(t, contentStr, "OTHER_VAR=other-value", "Other variables should be preserved")
}

// TestEnvExporterPreserveOtherVariables tests that other variables are preserved when updating
func TestEnvExporterPreserveOtherVariables(t *testing.T) {
	tmpDir := t.TempDir()
	envFilePath := filepath.Join(tmpDir, "env-test")

	// Create initial .env file with multiple variables
	initialContent := `DATABASE_URL=postgres://localhost
API_KEY=secret-key
SERVICE_TOKEN=old-value
LOG_LEVEL=info`
	err := os.WriteFile(envFilePath, []byte(initialContent), 0600)
	require.NoError(t, err, "Failed to create initial .env")

	exporter := NewEnvExporter(tmpDir)
	ctx := context.Background()

	// Update only SERVICE_TOKEN
	newToken := "new-service-token"
	export := Export{
		Type: ExportTypeEnv,
		File: "env-test",
		Name: "SERVICE_TOKEN",
	}

	err = exporter.Export(ctx, newToken, export)
	require.NoError(t, err, "Export should succeed")

	content, err := os.ReadFile(envFilePath)
	require.NoError(t, err, "Failed to read .env file")

	contentStr := string(content)

	// Verify all original variables are still present
	expectedVars := []string{
		"DATABASE_URL=postgres://localhost",
		"API_KEY=secret-key",
		"SERVICE_TOKEN=new-service-token",
		"LOG_LEVEL=info",
	}

	for _, expectedVar := range expectedVars {
		require.Contains(t, contentStr, expectedVar, "Variable should be preserved or updated")
	}
}

// TestEnvExporterEmptyLines tests handling of empty lines in .env file
func TestEnvExporterEmptyLines(t *testing.T) {
	tmpDir := t.TempDir()
	envFilePath := filepath.Join(tmpDir, "env-test")

	// Create initial .env file with empty lines
	initialContent := "VAR1=value1\n\nVAR2=value2\n"
	err := os.WriteFile(envFilePath, []byte(initialContent), 0600)
	require.NoError(t, err, "Failed to create initial .env")

	exporter := NewEnvExporter(tmpDir)
	ctx := context.Background()

	newVar := "new-value"
	export := Export{
		Type: ExportTypeEnv,
		File: "env-test",
		Name: "NEW_VAR",
	}

	err = exporter.Export(ctx, newVar, export)
	require.NoError(t, err, "Export should succeed")

	content, err := os.ReadFile(envFilePath)
	require.NoError(t, err, "Failed to read .env file")

	contentStr := string(content)

	// Verify all variables are present
	require.Contains(t, contentStr, "VAR1=value1", "VAR1 should be preserved")
	require.Contains(t, contentStr, "VAR2=value2", "VAR2 should be preserved")
	require.Contains(t, contentStr, "NEW_VAR=new-value", "NEW_VAR should be added")
}

// TestEnvExporterMultipleUpdates tests multiple sequential exports
func TestEnvExporterMultipleUpdates(t *testing.T) {
	tmpDir := t.TempDir()
	envFilePath := filepath.Join(tmpDir, "env-test")
	exporter := NewEnvExporter(tmpDir)
	ctx := context.Background()

	// First export
	export1 := Export{
		Type: ExportTypeEnv,
		File: "env-test",
		Name: "SERVICE_PYCK_TOKEN",
	}
	err := exporter.Export(ctx, "token-1", export1)
	require.NoError(t, err, "First export should succeed")

	// Second export (different variable)
	export2 := Export{
		Type: ExportTypeEnv,
		File: "env-test",
		Name: "SERVICE_WORKER_PYCK_TOKEN",
	}
	err = exporter.Export(ctx, "token-2", export2)
	require.NoError(t, err, "Second export should succeed")

	// Third export (update first variable)
	export3 := Export{
		Type: ExportTypeEnv,
		File: "env-test",
		Name: "SERVICE_PYCK_TOKEN",
	}
	err = exporter.Export(ctx, "token-1-updated", export3)
	require.NoError(t, err, "Third export should succeed")

	content, err := os.ReadFile(envFilePath)
	require.NoError(t, err, "Failed to read .env file")

	contentStr := string(content)
	lines := strings.Split(contentStr, "\n")

	// Count variables
	var serviceTokenLine, workerTokenLine string
	for _, line := range lines {
		if strings.HasPrefix(line, "SERVICE_PYCK_TOKEN=") {
			serviceTokenLine = line
		}
		if strings.HasPrefix(line, "SERVICE_WORKER_PYCK_TOKEN=") {
			workerTokenLine = line
		}
	}

	require.Equal(t, "SERVICE_PYCK_TOKEN=token-1-updated", serviceTokenLine, "SERVICE_TOKEN should be updated")
	require.Equal(t, "SERVICE_WORKER_PYCK_TOKEN=token-2", workerTokenLine, "WORKER_TOKEN should be added")

	// Verify no duplicates
	serviceTokenCount := strings.Count(contentStr, "SERVICE_PYCK_TOKEN=")
	require.Equal(t, 1, serviceTokenCount, "Should have exactly 1 SERVICE_PYCK_TOKEN entry")

	workerTokenCount := strings.Count(contentStr, "SERVICE_WORKER_PYCK_TOKEN=")
	require.Equal(t, 1, workerTokenCount, "Should have exactly 1 SERVICE_WORKER_PYCK_TOKEN entry")
}

// TestEnvExporterSpecialCharactersInValue tests handling special characters in credential values
func TestEnvExporterSpecialCharactersInValue(t *testing.T) {
	tmpDir := t.TempDir()
	envFilePath := filepath.Join(tmpDir, "env-test")
	exporter := NewEnvExporter(tmpDir)
	ctx := context.Background()

	// Credential with special characters
	credentials := "eyJhbGciOiJFUzI1NiIsImtpZCI6IjEifQ.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ"
	export := Export{
		Type: ExportTypeEnv,
		File: "env-test",
		Name: "JWT_TOKEN",
	}

	err := exporter.Export(ctx, credentials, export)
	require.NoError(t, err, "Export should succeed")

	content, err := os.ReadFile(envFilePath)
	require.NoError(t, err, "Failed to read .env file")

	expected := fmt.Sprintf("JWT_TOKEN=%s", credentials)
	require.Equal(t, expected, string(content), "Special characters should be preserved")
}

// TestEnvExporterFilePermissions tests that .env file has correct permissions
func TestEnvExporterFilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	envFilePath := filepath.Join(tmpDir, "env-test")
	exporter := NewEnvExporter(tmpDir)
	ctx := context.Background()

	export := Export{
		Type: ExportTypeEnv,
		File: "env-test",
		Name: "SECRET_KEY",
	}

	err := exporter.Export(ctx, "secret-value", export)
	require.NoError(t, err, "Export should succeed")

	fileInfo, err := os.Stat(envFilePath)
	require.NoError(t, err, "Failed to stat .env file")

	// Check permissions are 0644 (read/write for owner only)
	expectedPerms := os.FileMode(0o644)
	require.Equal(t, expectedPerms, fileInfo.Mode().Perm(), "File permissions should be 0644")
}

// TestEnvExporterInvalidPath tests error handling for invalid paths
func TestEnvExporterInvalidPath(t *testing.T) {
	// Use a non-existent directory
	exporter := NewEnvExporter("/nonexistent/path/that/does/not/exist")
	ctx := context.Background()

	export := Export{
		Type: ExportTypeEnv,
		File: "env-test",
		Name: "TEST_VAR",
	}

	err := exporter.Export(ctx, "test-value", export)
	require.Error(t, err, "Export should fail for invalid path")
}

// TestEnvExporterContextWithLogger tests that context logger is properly used
func TestEnvExporterContextWithLogger(t *testing.T) {
	tmpDir := t.TempDir()
	envFilePath := filepath.Join(tmpDir, "env-test")
	exporter := NewEnvExporter(tmpDir)

	// Create context with logger
	ctx := log.Context(context.Background(), log.DefaultLogger())

	export := Export{
		Type: ExportTypeEnv,
		File: "env-test",
		Name: "TEST_VAR",
	}

	err := exporter.Export(ctx, "test-value", export)
	require.NoError(t, err, "Export should succeed")

	// Verify the file was created
	content, err := os.ReadFile(envFilePath)
	require.NoError(t, err, "Failed to read .env file")

	require.Equal(t, "TEST_VAR=test-value", string(content), "File content should match")
}

// TestEnvExporterCaseSensitiveUpdate tests that variable name matching is case-sensitive
func TestEnvExporterCaseSensitiveUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	envFilePath := filepath.Join(tmpDir, "env-test")

	// Create initial .env file with lowercase variable
	initialContent := "service_token=old-value"
	err := os.WriteFile(envFilePath, []byte(initialContent), 0600)
	require.NoError(t, err, "Failed to create initial .env")

	exporter := NewEnvExporter(tmpDir)
	ctx := context.Background()

	// Export with uppercase variable name (should add new, not update)
	export := Export{
		Type: ExportTypeEnv,
		File: "env-test",
		Name: "SERVICE_TOKEN",
	}

	err = exporter.Export(ctx, "new-value", export)
	require.NoError(t, err, "Export should succeed")

	content, err := os.ReadFile(envFilePath)
	require.NoError(t, err, "Failed to read .env file")

	contentStr := string(content)

	// Both should exist (case-sensitive)
	require.Contains(t, contentStr, "service_token=old-value", "Original lowercase variable should be preserved")
	require.Contains(t, contentStr, "SERVICE_TOKEN=new-value", "Uppercase variable should be added")
}
