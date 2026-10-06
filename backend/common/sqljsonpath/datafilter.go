package sqljsonpath

import (
	"errors"
	"fmt"

	"entgo.io/ent/dialect/sql/sqljson"
)

// ErrInvalidDataFilter reports a JSON data filter whose shape its operator
// cannot use. The *WhereInput data filter resolvers used to skip the predicate
// for such a filter, and a query without the predicate returns the caller's
// whole table.
var ErrInvalidDataFilter = errors.New("invalid JSON data filter")

// PathValue splits a [path, value] data filter (Data, DataContains) into the
// validated path option and the value. Any other length is refused.
func PathValue(data []string) (sqljson.Option, string, error) {
	if len(data) != 2 {
		return nil, "", fmt.Errorf("%w: want [path, value], got %d elements", ErrInvalidDataFilter, len(data))
	}
	path, err := DotPath(data[0])
	if err != nil {
		return nil, "", err
	}
	return path, data[1], nil
}

// PathValues splits a [path, value, …] data filter (DataIn) into the validated
// path option and the values. Fewer than two elements are refused.
func PathValues(data []string) (sqljson.Option, []any, error) {
	if len(data) < 2 {
		return nil, nil, fmt.Errorf("%w: want [path, value, …], got %d elements", ErrInvalidDataFilter, len(data))
	}
	path, err := DotPath(data[0])
	if err != nil {
		return nil, nil, err
	}
	values := make([]any, 0, len(data)-1)
	for _, v := range data[1:] {
		values = append(values, v)
	}
	return path, values, nil
}

// KeyPath validates the key of a DataHasKey filter. An empty key is refused.
func KeyPath(key string) (sqljson.Option, error) {
	if key == "" {
		return nil, fmt.Errorf("%w: empty key", ErrInvalidDataFilter)
	}
	return DotPath(key)
}
