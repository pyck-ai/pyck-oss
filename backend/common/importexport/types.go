// Package importexport provides a generic, entity-agnostic mechanism for
// importing and exporting entities via the GraphQL API. It uses __typename to
// identify entity types and dispatches to per-entity operations registered by
// service clients.
package importexport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ErrImmutableField is returned when an import asks to change a field the
// update input cannot express because it is fixed at creation.
var ErrImmutableField = errors.New("field is fixed at creation")

// progressf writes best-effort progress output to the caller-supplied
// writer. Progress reporting must never abort an import or export, so a
// failing writer is deliberately ignored rather than propagated.
func progressf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...) //nolint:errcheck // best-effort progress output
}

// EntityDescriptor describes how to list, create, and update one entity type.
// Each importable entity type registers one descriptor with the [Registry].
type EntityDescriptor struct {
	// TypeName is the GraphQL __typename (e.g., "Location", "Repository").
	TypeName string

	// Service identifies which service owns this entity (e.g., "management").
	Service string

	// IdentityFields are the WhereInput fields that together uniquely identify
	// an entity within a tenant (e.g. ["name"], ["sku"], or ["slug","version"]
	// for a versioned DataType). They drive both $ref resolution and the
	// import-time existence check. Empty for create-only entities with no
	// natural key, whose existence is checked by "id" instead.
	IdentityFields []string

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

	// ImmutableFields names fields the create input accepts but the update
	// input cannot express, because they are fixed at creation. An import
	// carrying a different value for one of them on an existing entity is
	// refused: the update (or, for a create-only entity, the skip) would
	// otherwise report success while silently dropping the difference the
	// operator asked for.
	ImmutableFields []string

	// References are this entity's outgoing FK edges. They drive export
	// ordering (a referenced entity is emitted before its referrers) and the
	// rewrite of a raw FK id into a portable $ref. Federated FK targets cannot
	// be inferred from the schema, so edges are declared via @pyckImportable.
	References []Reference
}

// Reference is a declared FK edge: Field on the owning entity holds the id of an
// entity of type TargetType. The target's identity (for the $ref payload) is read
// from the target's own descriptor at runtime, so it is not repeated here.
type Reference struct {
	Field      string
	TargetType string
}

// identityKeySep separates identity-field values when building a cache key. It
// is a non-printable byte so it can never appear inside a field value.
const identityKeySep = "\x1f"

// identity extracts this descriptor's identity-field values from src — a
// record's data map or a $ref map — returning a WhereInput-shaped filter and a
// stable cache key. ok is false when the descriptor declares no identity fields
// or src is missing one of them (absent or explicitly nil) — a nil value can't
// build a usable filter (e.g. `{version: nil}`), matching buildReferenceIndex.
func (d *EntityDescriptor) identity(src map[string]any) (where map[string]any, key string, ok bool) {
	if len(d.IdentityFields) == 0 {
		return nil, "", false
	}
	where = make(map[string]any, len(d.IdentityFields))
	parts := make([]string, 0, len(d.IdentityFields))
	for _, f := range d.IdentityFields {
		v, present := src[f]
		if !present || v == nil {
			return nil, "", false
		}
		where[f] = v
		parts = append(parts, fmt.Sprint(v))
	}
	return where, strings.Join(parts, identityKeySep), true
}

// identityDisplay renders the identity fields as a deterministic "k=v" string
// for log and dry-run output.
func (d *EntityDescriptor) identityDisplay(where map[string]any) string {
	parts := make([]string, 0, len(d.IdentityFields))
	for _, f := range d.IdentityFields {
		parts = append(parts, fmt.Sprintf("%s=%v", f, where[f]))
	}
	return strings.Join(parts, " ")
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

// Unwrap exposes the cause so a caller can match it with errors.Is rather than
// on the formatted message.
func (e ImportError) Unwrap() error { return e.Err }
