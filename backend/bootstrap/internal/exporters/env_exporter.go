package exporters

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// EnvExporter exports credentials to a .env file
type EnvExporter struct {
	envPath string
}

// NewEnvExporter creates a new EnvExporter
func NewEnvExporter(envPath string) *EnvExporter {
	return &EnvExporter{
		envPath: envPath,
	}
}

// Exists checks if the env var is already set and non-empty in the .env file.
func (e *EnvExporter) Exists(_ context.Context, export Export) (bool, error) {
	envFilePath, err := safePath(e.envPath, export.File)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(envFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	prefix := export.Name + "="
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			value := strings.TrimPrefix(line, prefix)
			return value != "", nil
		}
	}
	return false, nil
}

// Export appends or updates an environment variable in a .env file
func (e *EnvExporter) Export(ctx context.Context, credentials string, export Export) error {
	logger := log.ForContext(ctx)

	envFilePath, err := safePath(e.envPath, export.File)
	if err != nil {
		return err
	}

	// Read existing file if it exists
	var existingContent string
	if data, err := os.ReadFile(envFilePath); err == nil {
		existingContent = string(data)
	}

	// Parse existing entries and update or append
	var lines []string
	if existingContent == "" {
		// New file - start with empty slice
		lines = []string{}
	} else {
		lines = strings.Split(existingContent, "\n")
	}

	found := false
	envEntry := fmt.Sprintf("%s=%s", export.Name, credentials)

	for i, line := range lines {
		if strings.HasPrefix(line, export.Name+"=") {
			lines[i] = envEntry
			found = true
			break
		}
	}

	if !found {
		lines = append(lines, envEntry)
	}

	// Write back to file
	newContent := strings.Join(lines, "\n")
	if err := os.WriteFile(envFilePath, []byte(newContent), 0o644); err != nil {
		return fmt.Errorf("failed to write to file %q: %w", envFilePath, err)
	}

	logger.Debug().Str("file", envFilePath).Msg("Saved credentials to env file")
	return nil
}
