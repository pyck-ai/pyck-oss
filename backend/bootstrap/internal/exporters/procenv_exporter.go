package exporters

import (
	"context"
	"fmt"
	"os"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// ProcessEnvExporter exports credentials to the process environment variables
type ProcessEnvExporter struct{}

// NewProcessEnvExporter creates a new ProcessEnvExporter
func NewProcessEnvExporter() *ProcessEnvExporter {
	return &ProcessEnvExporter{}
}

// Exists checks if the environment variable is set and non-empty in the current process.
func (e *ProcessEnvExporter) Exists(_ context.Context, export Export) (bool, error) {
	val, ok := os.LookupEnv(export.Name)
	return ok && val != "", nil
}

// Export sets the environment variable in the current process
func (e *ProcessEnvExporter) Export(ctx context.Context, credentials string, export Export) error {
	logger := log.ForContext(ctx)

	key := export.Name
	if key == "" {
		return fmt.Errorf("export name is required for process-env export")
	}

	if err := os.Setenv(key, credentials); err != nil {
		return fmt.Errorf("failed to set environment variable %q: %w", key, err)
	}

	logger.Debug().Str("key", key).Msg("Set environment variable")
	return nil
}
