package importexport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// serverManagedFields are fields stripped from export output by default. They
// are auto-populated by the server and not meaningful for import. dataTypeSlug
// is included because it is always re-derived from dataTypeID on import.
var serverManagedFields = []string{
	"id",
	"tenantID",
	"createdAt",
	"createdBy",
	"updatedAt",
	"updatedBy",
	"deletedAt",
	"deletedBy",
	"dataTypeSlug",
}

// Exporter serializes entities to JSONL format.
type Exporter struct {
	registry           *Registry
	output             io.Writer
	skipUnresolvedRefs bool
}

// ExporterOption configures an [Exporter].
type ExporterOption func(*Exporter)

// WithExportOutput sets the writer for progress messages. Defaults to os.Stderr.
func WithExportOutput(w io.Writer) ExporterOption {
	return func(e *Exporter) { e.output = w }
}

// WithSkipUnresolvedReferences makes the export complete when a row's
// reference cannot be resolved, omitting the reference and warning per row,
// instead of failing with ErrUnresolvedReference. Off by default: a skipped
// reference means the re-imported row loses its pin, so this is a recovery
// escape hatch (mirroring the importer's WithContinueOnError), not the
// normal mode.
func WithSkipUnresolvedReferences(skip bool) ExporterOption {
	return func(e *Exporter) { e.skipUnresolvedRefs = skip }
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

// Export writes entities as JSONL to the given writer. Records are emitted in
// dependency order (a referenced entity precedes its referrers), so the stream
// can be re-imported in a single pass. If typeNames is empty, all registered
// entity types are exported.
func (exp *Exporter) Export(ctx context.Context, w io.Writer, typeNames []string) error {
	descs := exp.descriptorsForTypes(typeNames)
	if len(descs) == 0 {
		return ErrNoEntityTypes
	}

	items, index, err := exp.plan(ctx, descs)
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	var unresolved []error
	for _, it := range items {
		record, err := exp.prepareRecord(it.desc, it.node, index)
		if err != nil {
			// Keep collecting so one run reports every dangling reference
			// instead of costing the operator one export per bad row.
			unresolved = append(unresolved, err)
			continue
		}
		if err := encoder.Encode(record); err != nil {
			return fmt.Errorf("encode %s: %w", it.desc.TypeName, err)
		}
	}
	return joinUnresolved(unresolved)
}

// ExportToDir writes each entity type to a separate .jsonl file in the given
// directory (lowercase typename, e.g. "location.jsonl"). Records within each
// file keep their dependency order (parents before children for self-referencing
// types). The directory must exist.
func (exp *Exporter) ExportToDir(ctx context.Context, dir string, typeNames []string) error {
	descs := exp.descriptorsForTypes(typeNames)
	if len(descs) == 0 {
		return ErrNoEntityTypes
	}

	items, index, err := exp.plan(ctx, descs)
	if err != nil {
		return err
	}

	buckets := make(map[string][]map[string]any, len(descs))
	var unresolved []error
	for _, it := range items {
		record, err := exp.prepareRecord(it.desc, it.node, index)
		if err != nil {
			unresolved = append(unresolved, err)
			continue
		}
		buckets[it.desc.TypeName] = append(buckets[it.desc.TypeName], record)
	}
	if err := joinUnresolved(unresolved); err != nil {
		return err
	}

	// One file per requested type, even when empty.
	for _, desc := range descs {
		path := filepath.Join(dir, strings.ToLower(desc.TypeName)+".jsonl")
		if err := writeRecords(path, buckets[desc.TypeName]); err != nil {
			return err
		}
	}
	return nil
}

// emitItem is one entity scheduled for export, paired with its descriptor.
type emitItem struct {
	desc *EntityDescriptor
	node map[string]any
}

// plan lists every requested type, builds the reference index used for $ref
// rewriting, and returns the entities in dependency order.
func (exp *Exporter) plan(ctx context.Context, descs []*EntityDescriptor) ([]emitItem, referenceIndex, error) {
	descByType := make(map[string]*EntityDescriptor, len(descs))
	nodesByType := make(map[string][]map[string]any, len(descs))
	for _, desc := range descs {
		descByType[desc.TypeName] = desc
		progressf(exp.output, "exporting %s...\n", desc.TypeName)
		nodes, err := paginateAll(ctx, desc, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("export %s: %w", desc.TypeName, err)
		}
		nodesByType[desc.TypeName] = nodes
		progressf(exp.output, "exported %d %s entities\n", len(nodes), desc.TypeName)
	}

	nodeByID := buildNodeIndex(nodesByType)
	refIndex := buildReferenceIndex(descByType, nodesByType)
	items := orderForExport(descByType, nodeByID, nodesByType)
	return items, refIndex, nil
}

// referenceIndex maps targetType → id → the $ref payload that resolves it.
type referenceIndex map[string]map[string]map[string]any

// buildNodeIndex maps type → id → node for dependency lookups during ordering.
func buildNodeIndex(nodesByType map[string][]map[string]any) map[string]map[string]map[string]any {
	index := make(map[string]map[string]map[string]any, len(nodesByType))
	for typeName, nodes := range nodesByType {
		byID := make(map[string]map[string]any, len(nodes))
		for _, node := range nodes {
			if id, ok := node["id"].(string); ok && id != "" {
				byID[id] = node
			}
		}
		index[typeName] = byID
	}
	return index
}

// buildReferenceIndex maps each keyed type's id → an identity $ref payload
// ({__typename, identity fields}). Keyless types (no IdentityFields) are
// skipped: references to them can't become identity $refs and stay raw.
func buildReferenceIndex(descByType map[string]*EntityDescriptor, nodesByType map[string][]map[string]any) referenceIndex {
	index := make(referenceIndex)
	for typeName, desc := range descByType {
		if len(desc.IdentityFields) == 0 {
			continue
		}
		byID := make(map[string]map[string]any)
		for _, node := range nodesByType[typeName] {
			id, ok := node["id"].(string)
			if !ok || id == "" {
				continue
			}
			payload := map[string]any{"__typename": typeName}
			complete := true
			for _, f := range desc.IdentityFields {
				v, present := node[f]
				if !present || v == nil {
					complete = false
					break
				}
				payload[f] = v
			}
			if complete {
				byID[id] = payload
			}
		}
		index[typeName] = byID
	}
	return index
}

// orderForExport returns entities in dependency order via a post-order DFS over
// declared references: an entity is emitted only after the entities it
// references. A visited/on-stack set guards against cycles (including
// self-references like a repository hierarchy, which resolves to parents-first).
// Types and rows are walked in sorted order for deterministic output.
func orderForExport(
	descByType map[string]*EntityDescriptor,
	nodeByID map[string]map[string]map[string]any,
	nodesByType map[string][]map[string]any,
) []emitItem {
	var items []emitItem
	visited := make(map[string]bool)
	onStack := make(map[string]bool)

	var visit func(typeName string, node map[string]any)
	visit = func(typeName string, node map[string]any) {
		id, _ := node["id"].(string)
		key := typeName + identityKeySep + id
		if visited[key] || onStack[key] {
			return
		}
		onStack[key] = true

		desc := descByType[typeName]
		if desc != nil {
			for _, ref := range desc.References {
				targets := nodeByID[ref.TargetType]
				if targets == nil {
					continue // target type not in this export set
				}
				for _, targetID := range referencedIDs(node[ref.Field]) {
					if targetNode, ok := targets[targetID]; ok {
						visit(ref.TargetType, targetNode)
					}
				}
			}
		}

		delete(onStack, key)
		visited[key] = true
		items = append(items, emitItem{desc: desc, node: node})
	}

	for _, typeName := range typesInDependencyOrder(descByType, nodesByType) {
		nodes := append([]map[string]any(nil), nodesByType[typeName]...)
		desc := descByType[typeName]
		sort.SliceStable(nodes, func(i, j int) bool {
			return nodeLess(desc, nodes[i], nodes[j])
		})
		for _, node := range nodes {
			visit(typeName, node)
		}
	}
	return items
}

// typesInDependencyOrder returns the export set's type names with referenced
// target types before their referrers (alphabetical among peers, so output is
// deterministic). Seeding the emit DFS in this order matters beyond
// aesthetics: an alphabetical seed lets a referrer sorting before its target
// type hoist the exact row it pins ahead of that type's remaining rows — for
// DataType that emits a higher version before a lower one, and the file then
// violates the append-only import contract (a pinned version at or below the
// family's MAX is rejected). Reference cycles between types are broken at the
// first revisited type; the per-row DFS still orders the rows themselves.
func typesInDependencyOrder(
	descByType map[string]*EntityDescriptor,
	nodesByType map[string][]map[string]any,
) []string {
	return dependencyOrder(sortedKeys(nodesByType), func(typeName string) []Reference {
		if desc := descByType[typeName]; desc != nil {
			return desc.References
		}
		return nil
	})
}

// nodeLess orders rows of one type by their IdentityFields, with the raw id
// as final tiebreak. Identity order is the contract the import side checks:
// DataType rows must ascend by (slug, version) or the server's append-only
// reconcile rejects the file on re-import.
func nodeLess(desc *EntityDescriptor, a, b map[string]any) bool {
	if desc != nil {
		for _, f := range desc.IdentityFields {
			if c := compareFieldValues(a[f], b[f]); c != 0 {
				return c < 0
			}
		}
	}
	id1, _ := a["id"].(string)
	id2, _ := b["id"].(string)
	return id1 < id2
}

// compareFieldValues compares two identity-field values: strings directly,
// numbers numerically, anything else via its printed form. Production nodes
// pass through BuildListResult's JSON round-trip, so numbers are float64;
// the int case serves hand-built maps in tests.
func compareFieldValues(a, b any) int {
	if sa, ok := a.(string); ok {
		if sb, ok := b.(string); ok {
			return strings.Compare(sa, sb)
		}
	}
	if fa, ok := toFloat(a); ok {
		if fb, ok := toFloat(b); ok {
			switch {
			case fa < fb:
				return -1
			case fa > fb:
				return 1
			default:
				return 0
			}
		}
	}
	return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
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

// prepareRecord strips server-managed fields and rewrites declared FK references
// into portable $refs. A create-only entity with a natural key drops its
// server-generated id (it is re-found by identity on import); a create-only
// entity without one keeps its id for skip-by-id dedup.
func (exp *Exporter) prepareRecord(desc *EntityDescriptor, node map[string]any, index referenceIndex) (map[string]any, error) {
	keepID := desc.Update == nil && len(desc.IdentityFields) == 0
	record := make(map[string]any, len(node)+1)
	record["__typename"] = desc.TypeName

	// A declared reference field is never copied as a raw FK uuid — a raw id has
	// no value in a portable export. It is re-added below only as a $ref, and
	// only when resolvable; an unresolvable or nil FK is simply omitted.
	refFields := make(map[string]bool, len(desc.References))
	for _, ref := range desc.References {
		refFields[ref.Field] = true
	}

	for k, v := range node {
		if k == "id" {
			if keepID {
				record[k] = v
			}
			continue
		}
		if refFields[k] || slices.Contains(serverManagedFields, k) {
			continue
		}
		record[k] = v
	}

	var unresolved []error
	for _, ref := range desc.References {
		err := rewriteReference(record, node, ref, index)
		if err == nil {
			continue
		}
		rowErr := fmt.Errorf("export %s (%s): %w", desc.TypeName, desc.identityDisplay(recordIdentity(desc, node)), err)
		if exp.skipUnresolvedRefs {
			progressf(exp.output, "WARNING: %s — reference omitted\n", rowErr)
			continue
		}
		unresolved = append(unresolved, rowErr)
	}
	if len(unresolved) > 0 {
		return nil, errors.Join(unresolved...)
	}
	return record, nil
}

// recordIdentity returns the row's identity fields for error display, falling
// back to the raw id when the descriptor has no identity.
func recordIdentity(desc *EntityDescriptor, node map[string]any) map[string]any {
	where, _, ok := desc.identity(node)
	if !ok {
		where = map[string]any{"id": node["id"]}
	}
	return where
}

// joinUnresolved folds every collected unresolved-reference error into one,
// headed by a count, so a single export run reports the full repair list.
// Each member wraps ErrUnresolvedReference, so errors.Is still matches.
func joinUnresolved(unresolved []error) error {
	if len(unresolved) == 0 {
		return nil
	}
	return fmt.Errorf("%d unresolved reference(s):\n%w", len(unresolved), errors.Join(unresolved...))
}

// rewriteReference replaces a raw FK id (scalar or list) with an identity $ref.
// The field was already stripped by prepareRecord, so a nil FK stays omitted,
// and a reference whose whole target TYPE is outside the export set is
// omitted too — that is a deliberate subset export, and a raw FK id has no
// portable value.
//
// A target type that IS in the set but whose row is missing is a different
// case: the row exists and points somewhere the export cannot see (a
// soft-deleted target is hidden from List), so omitting it would silently
// unpin the record. That fails the export with ErrUnresolvedReference.
func rewriteReference(record, node map[string]any, ref Reference, index referenceIndex) error {
	targets, inSet := index[ref.TargetType]
	if !inSet {
		return nil
	}
	switch raw := node[ref.Field].(type) {
	case string:
		if raw == "" {
			return nil
		}
		payload, ok := targets[raw]
		if !ok {
			return fmt.Errorf("%w: %s -> %s %q not in export", ErrUnresolvedReference, ref.Field, ref.TargetType, raw)
		}
		record[ref.Field] = map[string]any{"$ref": payload}
	case []any:
		out := make([]any, 0, len(raw))
		for _, elem := range raw {
			id, ok := elem.(string)
			if !ok {
				return fmt.Errorf("%w: %s -> %s element is %T, want a string id", ErrUnresolvedReference, ref.Field, ref.TargetType, elem)
			}
			payload, ok := targets[id]
			if !ok {
				return fmt.Errorf("%w: %s -> %s %q not in export", ErrUnresolvedReference, ref.Field, ref.TargetType, id)
			}
			out = append(out, map[string]any{"$ref": payload})
		}
		if len(out) > 0 {
			record[ref.Field] = out
		}
	}
	return nil
}

// referencedIDs extracts FK id strings from a node field that may be a scalar id
// or a list of ids.
func referencedIDs(v any) []string {
	switch x := v.(type) {
	case string:
		if x != "" {
			return []string{x}
		}
	case []any:
		ids := make([]string, 0, len(x))
		for _, elem := range x {
			if s, ok := elem.(string); ok && s != "" {
				ids = append(ids, s)
			}
		}
		return ids
	}
	return nil
}

func writeRecords(path string, records []map[string]any) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, f.Close())
	}()

	encoder := json.NewEncoder(f)
	encoder.SetEscapeHTML(false)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
