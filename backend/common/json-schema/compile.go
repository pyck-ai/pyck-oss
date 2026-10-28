package json_schema

import (
	"errors"
	"fmt"
	"strings"

	validator "github.com/santhosh-tekuri/jsonschema/v6"
)

// ErrInvalidSchema marks a document CompileString refuses: not JSON, or
// JSON that is not a structurally valid JSON Schema. The library answers
// with a different concrete type per defect (and none of them with a gRPC
// or HTTP status), so this sentinel is what a caller matches on to tell a
// document it must not accept from a fault of whatever produced it.
var ErrInvalidSchema = errors.New("invalid json schema")

// CompileString compiles a JSON Schema document from its string form under the
// given resource name. It returns an error wrapping ErrInvalidSchema if the
// document is not valid JSON or not a structurally valid JSON Schema.
func CompileString(name, schema string) (*validator.Schema, error) {
	doc, err := validator.UnmarshalJSON(strings.NewReader(schema))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSchema, err)
	}

	compiler := validator.NewCompiler()
	if err := compiler.AddResource(name, doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSchema, err)
	}

	compiled, err := compiler.Compile(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSchema, err)
	}

	return compiled, nil
}
