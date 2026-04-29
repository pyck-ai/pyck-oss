// Package importexport provides a generic, entity-agnostic mechanism for
// importing and exporting entities via the GraphQL API. It uses __typename to
// identify entity types and dispatches to per-entity operations registered by
// service clients.
package importexport

import (
	"context"
	"strconv"
)

// EntityDescriptor describes how to list, create, and update one entity type.
// Each importable entity type registers one descriptor with the [Registry].
type EntityDescriptor struct {
	// TypeName is the GraphQL __typename (e.g., "Location", "Repository").
	TypeName string

	// Service identifies which service owns this entity (e.g., "management").
	Service string

	// IdentityField is the WhereInput field used for existence checks during
	// import (e.g., "name", "slug", "sku"). This field must uniquely identify
	// an entity within a tenant.
	IdentityField string

	// List queries entities matching a filter. The where parameter is a
	// map that will be converted to the service's WhereInput type via JSON
	// round-trip. Pass nil for no filter. Returns a page of entities as
	// maps, along with pagination info.
	List func(ctx context.Context, after *string, first *int, where map[string]any) (ListResult, error)

	// Create creates a new entity. The input map uses GraphQL field names
	// matching the service's CreateInput type. Returns the created entity
	// as a map (must include "id").
	Create func(ctx context.Context, input map[string]any) (map[string]any, error)

	// Update updates an existing entity by ID. The input map uses GraphQL
	// field names matching the service's UpdateInput type. Returns the
	// updated entity as a map.
	Update func(ctx context.Context, id string, input map[string]any) (map[string]any, error)
}

// ListResult holds a page of entities from a List call.
type ListResult struct {
	// Nodes are the entities in this page, each as a map with GraphQL field names.
	Nodes []map[string]any

	// HasNextPage indicates whether more pages are available.
	HasNextPage bool

	// EndCursor is the cursor for fetching the next page. Nil when
	// HasNextPage is false.
	EndCursor *string
}

// ImportRecord represents a single entity parsed from an import file.
type ImportRecord struct {
	// TypeName is the __typename value from the record.
	TypeName string

	// Data contains all fields except __typename and $refid.
	Data map[string]any

	// Source is the file path this record was parsed from.
	Source string

	// Line is the 1-based line number within the source file (for JSONL),
	// or 0 for single-entity JSON files.
	Line int

	// RefID is a local alias assigned via "$refid" in the import data.
	// If set, subsequent records can reference this entity using
	// {"$ref": "<alias>"} instead of querying by identity field.
	RefID string
}

// ImportResult summarizes the outcome of an import operation.
type ImportResult struct {
	Created int
	Updated int
	Skipped int
	Errors  []ImportError
}

// ImportError records a failure for a specific import record.
type ImportError struct {
	Record ImportRecord
	Err    error
}

func (e ImportError) Error() string {
	if e.Record.Line > 0 {
		return e.Record.Source + ":" + strconv.Itoa(e.Record.Line) + ": " + e.Err.Error()
	}
	return e.Record.Source + ": " + e.Err.Error()
}
