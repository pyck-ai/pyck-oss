//go:generate -command enumer go tool enumer -text -json -yaml -typederrors

package exporters

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// ExportType defines the type of credential export target.
//
//go:generate enumer -output=exporttype_gen.go -type=ExportType -linecomment
type ExportType uint

const (
	ExportTypeFile       ExportType = iota + 1 // file
	ExportTypeEnv                              // env
	ExportTypeK8s                              // k8s
	ExportTypeProcessEnv                       // process-env
)

type (
	// Exporter is the interface for exporting credentials
	Exporter interface {
		Export(ctx context.Context, credentials string, export Export) error
		Exists(ctx context.Context, export Export) (bool, error)
	}

	// ExporterRegistry manages available exporters and dispatches to them
	ExporterRegistry struct {
		exporters map[ExportType]Exporter
	}

	// Export defines how and where to export credentials
	Export struct {
		// Type is the export method (file, env, k8s, process-env).
		Type ExportType `yaml:"type"`

		// File is the target file name for file and env exports
		File string `yaml:"file"`

		// Name is the field name
		Name string `yaml:"name"`

		// Field selects which entity attribute to export (e.g. "id", "name").
		// For organizations and projects this is required.
		// For apps and machine users this is optional; when empty the
		// generated credential (key/token) is exported.
		Field string `yaml:"field,omitempty"`
	}
)

// safePath resolves export.File relative to basePath and ensures the result
// does not escape the base directory (e.g. via "../../etc/shadow").
func safePath(basePath, file string) (string, error) {
	resolved := filepath.Join(basePath, file)
	cleaned := filepath.Clean(resolved)
	base := filepath.Clean(basePath)

	if !strings.HasPrefix(cleaned, base+string(filepath.Separator)) && cleaned != base {
		return "", fmt.Errorf("export file path %q escapes base directory %q", file, basePath)
	}
	return cleaned, nil
}

// NewExporterRegistry creates a new registry with the provided exporters
func NewExporterRegistry(exporters map[ExportType]Exporter) *ExporterRegistry {
	return &ExporterRegistry{
		exporters: exporters,
	}
}

// CredentialsExist checks whether any of the given export guards already have credentials present.
// Returns true if at least one guard target exists, meaning key generation should be skipped.
func (r *ExporterRegistry) CredentialsExist(ctx context.Context, guards []*Export) (bool, error) {
	for _, guard := range guards {
		exporter, ok := r.exporters[guard.Type]
		if !ok {
			return false, fmt.Errorf("unknown export type: %s", guard.Type)
		}
		exists, err := exporter.Exists(ctx, *guard)
		if err != nil {
			return false, fmt.Errorf("checking existence for %s: %w", guard.Type, err)
		}
		if exists {
			log.ForContext(ctx).Debug().
				Str("type", guard.Type.String()).
				Str("file", guard.File).
				Str("name", guard.Name).
				Msg("Credentials already exist, skipping generation")
			return true, nil
		}
	}
	return false, nil
}

// Export exports credentials using the appropriate exporter
func (r *ExporterRegistry) Export(ctx context.Context, credentials string, export Export) error {
	exporter, ok := r.exporters[export.Type]
	if !ok {
		return fmt.Errorf("unknown export type: %s", export.Type)
	}

	log.ForContext(ctx).Debug().
		Str("type", export.Type.String()).
		Str("file", export.File).
		Msg("Exporting credentials")
	return exporter.Export(ctx, credentials, export)
}
