package exporters

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFileExporterNewFile tests writing credentials to a new file
func TestFileExporterNewFile(t *testing.T) {
	tmpDir := t.TempDir()
	exporter := NewFileExporter(tmpDir)
	ctx := context.Background()

	credentials := `{
  "type": "serviceaccount",
  "project_id": "test-project",
  "private_key_id": "key123"
}`
	export := Export{
		Type: ExportTypeFile,
		File: "test-credentials.json",
	}

	err := exporter.Export(ctx, credentials, export)
	require.NoError(t, err, "Export should succeed")

	// Verify file was created with correct content
	filePath := filepath.Join(tmpDir, "test-credentials.json")
	content, err := os.ReadFile(filePath)
	require.NoError(t, err, "Failed to read exported file")

	require.Equal(t, credentials, string(content), "File content should match credentials")
}

// TestFileExporterOverwrite tests overwriting existing credentials
func TestFileExporterOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test-credentials.json")

	// Write initial content
	oldContent := "old credentials"
	err := os.WriteFile(filePath, []byte(oldContent), 0600)
	require.NoError(t, err, "Failed to create initial file")

	exporter := NewFileExporter(tmpDir)
	ctx := context.Background()
	newCredentials := "new credentials"
	export := Export{
		Type: ExportTypeFile,
		File: "test-credentials.json",
	}

	err = exporter.Export(ctx, newCredentials, export)
	require.NoError(t, err, "Export should succeed")

	// Verify file was overwritten
	content, err := os.ReadFile(filePath)
	require.NoError(t, err, "Failed to read exported file")

	require.Equal(t, newCredentials, string(content), "File should be overwritten")
}

// TestFileExporterPermissions tests that exported files have correct permissions
func TestFileExporterPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	exporter := NewFileExporter(tmpDir)
	ctx := context.Background()

	credentials := "sensitive-credentials"
	export := Export{
		Type: ExportTypeFile,
		File: "secure-file.json",
	}

	err := exporter.Export(ctx, credentials, export)
	require.NoError(t, err, "Export should succeed")

	filePath := filepath.Join(tmpDir, "secure-file.json")
	fileInfo, err := os.Stat(filePath)
	require.NoError(t, err, "Failed to stat file")

	// Check permissions are 0644 (read/write for owner, read for group/others)
	expectedPerms := os.FileMode(0644)
	require.Equal(t, expectedPerms, fileInfo.Mode().Perm(), "File permissions should be 0644")
}

// TestFileExporterSpecialCharacters tests handling special characters in filename
func TestFileExporterSpecialCharacters(t *testing.T) {
	tmpDir := t.TempDir()
	exporter := NewFileExporter(tmpDir)
	ctx := context.Background()

	credentials := "special-credentials"
	export := Export{
		Type: ExportTypeFile,
		File: "service-worker-user.json",
	}

	err := exporter.Export(ctx, credentials, export)
	require.NoError(t, err, "Export should succeed")

	filePath := filepath.Join(tmpDir, "service-worker-user.json")
	content, err := os.ReadFile(filePath)
	require.NoError(t, err, "Failed to read exported file")

	require.Equal(t, credentials, string(content), "File content should match credentials")
}

// TestFileExporterInvalidPath tests error handling for invalid paths
func TestFileExporterInvalidPath(t *testing.T) {
	// Use a non-existent directory
	exporter := NewFileExporter("/nonexistent/path/that/does/not/exist")
	ctx := context.Background()

	credentials := "test-credentials"
	export := Export{
		Type: ExportTypeFile,
		File: "test.json",
	}

	err := exporter.Export(ctx, credentials, export)
	require.Error(t, err, "Export should fail for invalid path")
}
