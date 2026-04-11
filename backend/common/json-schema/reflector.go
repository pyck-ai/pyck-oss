package json_schema

// TODO: This reflector and validator should be merged with the common/validator
// package. Both of them implement schema validation with the only exception
// that the common/validator works with datatypes via slugs/ids and is backed by
// a datatype cache. Merging the schema reflection, caching, and validation into
// one package would greatly improve maintainability.

import (
	"encoding/json"
	"fmt"
	"reflect"

	validator "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/swaggest/jsonschema-go"
)

var (
	// ErrInvalidInput is returned when the input to Reflect is invalid.
	ErrInvalidInput = fmt.Errorf("invalid input")
)

// Reflect generates a JSON Schema for the given type. The parameter v is
// typically a zero value of the type to reflect.  The returned Schema can be
// used to validate instances of the type. An error is returned if the
// reflection or compilation fails.
// For example:
//
//	schema, err := Reflect(MyType{})
//	if err != nil {
//	    // handle error
//	}
//
//	err = schema.Validate(myInstance)
//	if err != nil {
//	    // instance is invalid
//	}
func Reflect(v any) (*Schema, error) {
	var reflector jsonschema.Reflector

	typ := reflect.TypeOf(v) // unqualified name
	if typ == nil {
		return nil, ErrInvalidInput
	}

	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	schema, err := reflector.Reflect(v)
	if err != nil {
		return nil, fmt.Errorf("reflect: %w", err)
	}

	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	var s Schema

	if err := s.UnmarshalJSON(schemaJSON); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	return &s, nil
}

func MustReflect(v any) *Schema {
	s, err := Reflect(v)
	if err != nil {
		panic(err)
	}

	return s
}

type Schema struct {
	schema     jsonschema.Schema
	schemaJSON []byte
	validator  *validator.Schema
}

func (s *Schema) Bytes() []byte {
	return s.schemaJSON
}

func (s *Schema) Validate(data any) error {
	return s.validator.Validate(data)
}

func (s *Schema) MarshalJSON() ([]byte, error) {
	if s.schemaJSON == nil {
		return []byte("null"), nil
	}

	return s.schemaJSON, nil
}

func (s *Schema) UnmarshalJSON(schemaJSON []byte) error {
	compiler := validator.NewCompiler()
	compiler.AssertFormat()  // Enable format validation
	compiler.AssertContent() // Enable content validation
	compiler.AssertVocabs()  // Enable vocabulary validation

	var schema jsonschema.Schema

	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	var schemaMap map[string]any
	if err := json.Unmarshal(schemaJSON, &schemaMap); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	url := "urn:jsonschema:unmarshaled"
	if err := compiler.AddResource(url, schemaMap); err != nil {
		return fmt.Errorf("add resource: %w", err)
	}

	validator, err := compiler.Compile(url)
	if err != nil {
		return fmt.Errorf("compile: %w", err)
	}

	*s = Schema{
		schema:     schema,
		schemaJSON: schemaJSON,
		validator:  validator,
	}

	return nil
}
