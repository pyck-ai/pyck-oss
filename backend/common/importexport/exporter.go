package importexport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// serverManagedFields are fields that should be stripped from export output
// by default. These are auto-populated by the server and not meaningful for
// import.
var serverManagedFields = []string{
	"id",
	"tenantID",
	"createdAt",
	"createdBy",
	"updatedAt",
	"updatedBy",
	"deletedAt",
	"deletedBy",
}

// Exporter serializes entities to JSONL format.
type Exporter struct {
	registry *Registry
	output   io.Writer
}

// ExporterOption configures an [Exporter].
type ExporterOption func(*Exporter)

// WithExportOutput sets the writer for progress messages. Defaults to os.Stderr.
func WithExportOutput(w io.Writer) ExporterOption {
	return func(e *Exporter) { e.output = w }
}

// NewExporter creates an exporter backed by the given registry.
func NewExporter(registry *Registry, opts ...ExporterOption) *Exporter {
	exp := &Exporter{
		registry: registry,
		output:   os.Stderr,
	}
	for _, opt := range opts {
		opt(exp)
	}
	return exp
}

// Export writes entities as JSONL to the given writer. All matching entities
// are written to a single stream. If typeNames is empty, all registered entity
// types are exported.
func (exp *Exporter) Export(ctx context.Context, w io.Writer, typeNames []string) error {
	descs := exp.descriptorsForTypes(typeNames)
	if len(descs) == 0 {
		return ErrNoEntityTypes
	}

	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)

	for _, desc := range descs {
		if err := exp.exportType(ctx, desc, encoder); err != nil {
			return err
		}
	}

	return nil
}

// ExportToDir writes each entity type to a separate .jsonl file in the given
// directory. Files are named as lowercase typename (e.g., "location.jsonl").
// The directory must exist.
func (exp *Exporter) ExportToDir(ctx context.Context, dir string, typeNames []string) error {
	descs := exp.descriptorsForTypes(typeNames)
	if len(descs) == 0 {
		return ErrNoEntityTypes
	}

	for _, desc := range descs {
		filename := strings.ToLower(desc.TypeName) + ".jsonl"
		path := filepath.Join(dir, filename)

		if err := exp.exportTypeToFile(ctx, desc, path); err != nil {
			return err
		}
	}

	return nil
}

func (exp *Exporter) exportTypeToFile(ctx context.Context, desc *EntityDescriptor, path string) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	encoder := json.NewEncoder(f)
	encoder.SetEscapeHTML(false)

	return exp.exportType(ctx, desc, encoder)
}

func (exp *Exporter) exportType(ctx context.Context, desc *EntityDescriptor, encoder *json.Encoder) error {
	fmt.Fprintf(exp.output, "exporting %s...\n", desc.TypeName)

	nodes, err := paginateAll(ctx, desc, nil)
	if err != nil {
		return fmt.Errorf("export %s: %w", desc.TypeName, err)
	}

	for _, node := range nodes {
		record := exp.prepareRecord(desc, node)
		if err := encoder.Encode(record); err != nil {
			return fmt.Errorf("encode %s: %w", desc.TypeName, err)
		}
	}

	fmt.Fprintf(exp.output, "exported %d %s entities\n", len(nodes), desc.TypeName)
	return nil
}

func (exp *Exporter) descriptorsForTypes(typeNames []string) []*EntityDescriptor {
	if len(typeNames) == 0 {
		return exp.registry.All()
	}

	var descs []*EntityDescriptor
	for _, name := range typeNames {
		if desc, ok := exp.registry.Get(name); ok {
			descs = append(descs, desc)
		}
	}
	return descs
}

func (exp *Exporter) prepareRecord(desc *EntityDescriptor, node map[string]any) map[string]any {
	createOnly := desc.Update == nil
	record := make(map[string]any, len(node)+1)
	record["__typename"] = desc.TypeName

	for k, v := range node {
		if exp.shouldSkipField(k, createOnly) {
			continue
		}
		record[k] = v
	}
	return record
}

func (exp *Exporter) shouldSkipField(field string, createOnly bool) bool {
	// Create-only entities keep their ID so they can be re-imported
	// without duplication (importer skips existing by ID).
	if field == "id" && createOnly {
		return false
	}
	return slices.Contains(serverManagedFields, field)
}
