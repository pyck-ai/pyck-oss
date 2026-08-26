package json_schema

import (
	"strings"

	validator "github.com/santhosh-tekuri/jsonschema/v6"
)

// CompileString compiles a JSON Schema document from its string form under the
// given resource name. It returns an error if the document is not valid JSON
// or not a structurally valid JSON Schema.
func CompileString(name, schema string) (*validator.Schema, error) {
	doc, err := validator.UnmarshalJSON(strings.NewReader(schema))
	if err != nil {
		return nil, err
	}

	compiler := validator.NewCompiler()
	if err := compiler.AddResource(name, doc); err != nil {
		return nil, err
	}

	return compiler.Compile(name)
}
