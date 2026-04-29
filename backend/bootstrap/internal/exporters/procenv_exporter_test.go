package exporters

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessEnvExporter_Export(t *testing.T) {
	exporter := NewProcessEnvExporter()
	ctx := context.Background()

	key := "TEST_PROC_ENV_VAR"
	value := "test-value"

	// Cleanup
	os.Unsetenv(key)
	defer os.Unsetenv(key)

	export := Export{
		Type: ExportTypeProcessEnv,
		Name: key,
	}

	err := exporter.Export(ctx, value, export)
	require.NoError(t, err)

	got, exists := os.LookupEnv(key)
	assert.True(t, exists)
	assert.Equal(t, value, got)
}

func TestProcessEnvExporter_Overwrite(t *testing.T) {
	exporter := NewProcessEnvExporter()
	ctx := context.Background()

	key := "TEST_PROC_ENV_VAR_OVERWRITE"
	oldValue := "old-value"
	newValue := "new-value"

	// Setup
	os.Setenv(key, oldValue)
	defer os.Unsetenv(key)

	export := Export{
		Type: ExportTypeProcessEnv,
		Name: key,
	}

	err := exporter.Export(ctx, newValue, export)
	require.NoError(t, err)

	got, exists := os.LookupEnv(key)
	assert.True(t, exists)
	assert.Equal(t, newValue, got)
}

func TestProcessEnvExporter_MissingName(t *testing.T) {
	exporter := NewProcessEnvExporter()
	ctx := context.Background()

	export := Export{
		Type: ExportTypeProcessEnv,
		Name: "", // Missing name
	}

	err := exporter.Export(ctx, "value", export)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "export name is required")
}
