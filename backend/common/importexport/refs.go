package importexport

import (
	"context"
	"fmt"
)

// RefResolver resolves $ref references in import data. It maintains a local
// cache of previously resolved or imported entities to avoid redundant queries.
// It also supports local aliases via $refid for single-pass import of entities
// without identity fields.
type RefResolver struct {
	registry *Registry

	// cache maps "TypeName:identityValue" → entity ID.
	cache map[string]string

	// aliases maps "$refid alias" → entity ID for local references
	// within a single import run.
	aliases map[string]string
}

// NewRefResolver creates a resolver backed by the given registry.
func NewRefResolver(registry *Registry) *RefResolver {
	return &RefResolver{
		registry: registry,
		cache:    make(map[string]string),
		aliases:  make(map[string]string),
	}
}

// Track records an entity in the local cache so that subsequent $ref
// resolutions can find it without querying the API. Called after a successful
// create or update.
func (r *RefResolver) Track(typeName string, data map[string]any, id string) {
	desc, ok := r.registry.Get(typeName)
	if !ok {
		return
	}
	if val, ok := data[desc.IdentityField]; ok {
		r.cache[cacheKey(typeName, fmt.Sprint(val))] = id
	}
}

// TrackAlias records a $refid alias → entity ID mapping. Returns an error
// if the alias was already registered (duplicate).
func (r *RefResolver) TrackAlias(alias, id string) error {
	if _, exists := r.aliases[alias]; exists {
		return fmt.Errorf("%w %q", ErrDuplicateAlias, alias)
	}
	r.aliases[alias] = id
	return nil
}

// ResolveRefs walks all fields in data and replaces any $ref values with the
// resolved entity ID. A $ref can be:
//   - map[string]any: resolve by __typename + identity field query (existing)
//   - string: resolve by local $refid alias (new)
//
// This modifies data in-place.
func (r *RefResolver) ResolveRefs(ctx context.Context, data map[string]any) error {
	for key, val := range data {
		switch v := val.(type) {
		case map[string]any:
			refTarget, ok := v["$ref"]
			if !ok {
				continue
			}
			id, err := r.resolveRef(ctx, key, refTarget)
			if err != nil {
				return err
			}
			data[key] = id
		case []any:
			for i, elem := range v {
				m, ok := elem.(map[string]any)
				if !ok {
					continue
				}
				refTarget, ok := m["$ref"]
				if !ok {
					continue
				}
				id, err := r.resolveRef(ctx, fmt.Sprintf("%s[%d]", key, i), refTarget)
				if err != nil {
					return err
				}
				v[i] = id
			}
		}
	}
	return nil
}

// resolveRef dispatches $ref resolution based on the value type:
//   - string → alias lookup
//   - map[string]any → identity field query
func (r *RefResolver) resolveRef(ctx context.Context, fieldName string, refTarget any) (string, error) {
	switch ref := refTarget.(type) {
	case string:
		return r.resolveAlias(fieldName, ref)
	case map[string]any:
		return r.resolve(ctx, fieldName, ref)
	default:
		return "", fmt.Errorf("field %q: %w", fieldName, ErrRefInvalidValue)
	}
}

func (r *RefResolver) resolveAlias(fieldName, alias string) (string, error) {
	id, ok := r.aliases[alias]
	if !ok {
		return "", fmt.Errorf("field %q: %w %q", fieldName, ErrUnknownAlias, alias)
	}
	return id, nil
}

func (r *RefResolver) resolve(ctx context.Context, fieldName string, ref map[string]any) (string, error) {
	typeNameRaw, ok := ref["__typename"]
	if !ok {
		return "", fmt.Errorf("field %q: %w", fieldName, ErrRefMissingTypename)
	}
	typeName, ok := typeNameRaw.(string)
	if !ok || typeName == "" {
		return "", fmt.Errorf("field %q: %w", fieldName, ErrRefEmptyTypename)
	}

	desc, ok := r.registry.Get(typeName)
	if !ok {
		return "", fmt.Errorf("field %q: %w %q", fieldName, ErrRefUnknownType, typeName)
	}

	// Extract the identity value from the ref.
	identityVal, ok := ref[desc.IdentityField]
	if !ok {
		return "", fmt.Errorf("field %q: %w %q for %q", fieldName, ErrRefMissingIdentity, desc.IdentityField, typeName)
	}
	identityStr := fmt.Sprint(identityVal)

	// Check local cache first.
	key := cacheKey(typeName, identityStr)
	if id, ok := r.cache[key]; ok {
		return id, nil
	}

	// Query the API.
	where := map[string]any{desc.IdentityField: identityVal}
	first := 2 // Fetch 2 to detect ambiguity.
	result, err := desc.List(ctx, nil, &first, where)
	if err != nil {
		return "", fmt.Errorf("field %q: resolve $ref %s{%s=%q}: %w",
			fieldName, typeName, desc.IdentityField, identityVal, err)
	}

	switch len(result.Nodes) {
	case 0:
		return "", fmt.Errorf("field %q: %w: no %s with %s=%q",
			fieldName, ErrRefNotFound, typeName, desc.IdentityField, identityStr)
	case 1:
		id, ok := result.Nodes[0]["id"].(string)
		if !ok {
			return "", fmt.Errorf("field %q: %q: %w", fieldName, typeName, ErrRefNoID)
		}
		r.cache[key] = id
		return id, nil
	default:
		return "", fmt.Errorf("field %q: %w: multiple %s entities match %s=%q",
			fieldName, ErrRefAmbiguous, typeName, desc.IdentityField, identityStr)
	}
}

func cacheKey(typeName, identityValue string) string {
	return typeName + ":" + identityValue
}
