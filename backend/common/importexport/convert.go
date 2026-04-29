package importexport

import (
	"encoding/json"
)

// MapToStruct converts a map[string]any to a typed struct via JSON round-trip.
// The target type T must have json tags matching the map keys (which are
// GraphQL field names). This handles all type coercion including string→*string,
// float64→*int, nested structs, and enums.
func MapToStruct[T any](m map[string]any) (T, error) {
	var result T
	data, err := json.Marshal(m)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(data, &result)
	return result, err
}

// StructToMap converts a typed struct to map[string]any via JSON round-trip.
// Fields with json:"omitempty" that are zero-valued will be omitted.
func StructToMap(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	err = json.Unmarshal(data, &m)
	return m, err
}

// BuildListResult converts a slice of typed edge nodes and pagination info
// into a [ListResult]. Each node is converted to map[string]any via JSON
// round-trip. This is a helper for entity registration closures.
func BuildListResult[N any](nodes []N, hasNextPage bool, endCursor *string) (ListResult, error) {
	maps := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		m, err := StructToMap(node)
		if err != nil {
			return ListResult{}, err
		}
		maps = append(maps, m)
	}

	return ListResult{
		Nodes:       maps,
		HasNextPage: hasNextPage,
		EndCursor:   endCursor,
	}, nil
}
