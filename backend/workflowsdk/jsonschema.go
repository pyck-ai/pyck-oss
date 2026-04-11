package workflowsdk

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	validator "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/swaggest/jsonschema-go"
)

// CompileJSONSchema generates and compiles a JSON schema for the given value.
// The value should be a struct or a type that can be reflected into a JSON
// schema. It returns the compiled JSON schema or an error if the process fails.
// The generated schema is namespaced based on the package path and type name of
// the provided value. For example, for a type `MyType` in package
// `github.com/example/mypackage`, the schema will be registered under the name
// `github.com/example/mypackage/mytype.json`.
func CompileJSONSchema(v any) (*validator.Schema, error) {
	var (
		schemaMap map[string]any
		reflector jsonschema.Reflector
	)

	typ := reflect.TypeOf(v)
	typeName := "schema.json" // default

	if typ.PkgPath() != "" && typ.Name() != "" {
		typeName = strings.ToLower(fmt.Sprintf("%s/%s.json", typ.PkgPath(), typ.Name()))
	}

	schema, err := reflector.Reflect(v)
	if err != nil {
		return nil, fmt.Errorf("failed to generate JSON schema: %w", err)
	}

	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal schema: %w", err)
	}

	if err := json.Unmarshal(schemaBytes, &schemaMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal schema: %w", err)
	}

	compiler := validator.NewCompiler()
	if err := compiler.AddResource(typeName, schemaMap); err != nil {
		return nil, fmt.Errorf("failed to add schema resource: %w", err)
	}

	compiledSchema, err := compiler.Compile(typeName)
	if err != nil {
		return nil, fmt.Errorf("failed to compile schema: %w", err)
	}

	return compiledSchema, nil
}

// MustCompileJSONSchema is like CompileJSONSchema but panics if the schema
// cannot be compiled. It is intended for use in package initialization where
// failure to compile the schema should halt execution.
func MustCompileJSONSchema(v any) *validator.Schema {
	schema, err := CompileJSONSchema(v)
	if err != nil {
		panic("failed to compile JSON schema: " + err.Error())
	}

	return schema
}
