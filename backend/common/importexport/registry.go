package importexport

import (
	"fmt"
	"sort"
	"sync"
)

// Registry holds [EntityDescriptor] entries keyed by GraphQL __typename. Each
// service registers its importable entities at startup via [Register]. The
// importer and exporter use the registry to dispatch operations to the correct
// service client without knowing any entity-specific details.
type Registry struct {
	mu          sync.RWMutex
	descriptors map[string]*EntityDescriptor
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		descriptors: make(map[string]*EntityDescriptor),
	}
}

// Register adds an entity descriptor to the registry. It returns an error if
// the TypeName is empty or already registered.
func (r *Registry) Register(desc *EntityDescriptor) error {
	if desc.TypeName == "" {
		return ErrEmptyTypeName
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.descriptors[desc.TypeName]; exists {
		return fmt.Errorf("%w %q", ErrDuplicateRegistration, desc.TypeName)
	}
	r.descriptors[desc.TypeName] = desc
	return nil
}

// Get returns the descriptor for the given __typename, or nil and false if not
// registered.
func (r *Registry) Get(typeName string) (*EntityDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	desc, ok := r.descriptors[typeName]
	return desc, ok
}

// All returns all registered descriptors sorted by TypeName.
func (r *Registry) All() []*EntityDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*EntityDescriptor, 0, len(r.descriptors))
	for _, desc := range r.descriptors {
		result = append(result, desc)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].TypeName < result[j].TypeName
	})
	return result
}

// TypeNames returns all registered type names sorted alphabetically.
func (r *Registry) TypeNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.descriptors))
	for name := range r.descriptors {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TypeNamesInDependencyOrder returns all registered type names with each
// type's reference targets before it (alphabetical among peers, so the order
// is deterministic). Import consumes records in stream order and resolves a
// $ref by querying its target, so a referrer read before its target cannot
// resolve.
func (r *Registry) TypeNamesInDependencyOrder() []string {
	names := r.TypeNames()

	r.mu.RLock()
	defer r.mu.RUnlock()

	return dependencyOrder(names, func(typeName string) []Reference {
		if desc, ok := r.descriptors[typeName]; ok {
			return desc.References
		}
		return nil
	})
}

// dependencyOrder topologically orders names so that a name's reference
// targets precede it, preserving the caller's order among independent peers.
// Targets outside names are ignored, self-references are skipped (they order
// rows within a type, not the types themselves), and a reference cycle breaks
// at the first revisited type rather than recursing.
func dependencyOrder(names []string, refsOf func(string) []Reference) []string {
	inSet := make(map[string]bool, len(names))
	for _, name := range names {
		inSet[name] = true
	}

	order := make([]string, 0, len(names))
	visited := make(map[string]bool, len(names))

	var visit func(typeName string)
	visit = func(typeName string) {
		if visited[typeName] {
			return
		}
		visited[typeName] = true
		for _, ref := range refsOf(typeName) {
			if ref.TargetType == typeName || !inSet[ref.TargetType] {
				continue
			}
			visit(ref.TargetType)
		}
		order = append(order, typeName)
	}

	for _, name := range names {
		visit(name)
	}
	return order
}
