package std

import (
	"fmt"
	"reflect"
	"strings"
)

func MapToGraphQLString(m map[string]interface{}) string {
	if len(m) == 0 {
		return "{}"
	}

	var sb strings.Builder
	sb.WriteString("{")

	for key, value := range m {
		sb.WriteString(fmt.Sprintf("%s:", key))
		sb.WriteString(valueToGraphQLString(value))
		sb.WriteString(",")
	}

	// Remove the trailing comma
	query := sb.String()
	query = query[:len(query)-1] + "}"

	return query
}

// Helper function to convert different types to GraphQL string format
func valueToGraphQLString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return fmt.Sprintf("\"%s\"", v)
	case float64, int, int64, bool:
		return fmt.Sprintf("%v", v)
	case []interface{}:
		var result []string
		for _, elem := range v {
			result = append(result, valueToGraphQLString(elem))
		}
		return "[" + strings.Join(result, ",") + "]"
	case []string:
		var result []string
		for _, elem := range v {
			result = append(result, fmt.Sprintf("\"%s\"", elem))
		}
		return "[" + strings.Join(result, ",") + "]"
	case map[string]interface{}:
		return MapToGraphQLString(v)
	default:
		// Handle unknown types (could be a struct, pointer, etc.)
		rv := reflect.ValueOf(value)
		if rv.Kind() == reflect.Map {
			return MapToGraphQLString(value.(map[string]interface{}))
		}
		return "\"\""
	}
}
