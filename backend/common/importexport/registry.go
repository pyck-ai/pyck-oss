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
