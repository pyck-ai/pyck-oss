package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// loadSchema loads and parses all GraphQL schema files from the specified directory
func loadSchema(schemaDir string) (*ast.Schema, error) {
	// Find all .graphql files in the schema directory
	files, err := filepath.Glob(filepath.Join(schemaDir, "*.graphql"))
	if err != nil {
		return nil, fmt.Errorf("failed to find schema files: %w", err)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoGraphQLFiles, schemaDir)
	}

	if verbose {
		fmt.Printf("Found %d schema files\n", len(files))
	}

	// Load all schema sources
	sources := make([]*ast.Source, 0, len(files)+1)

	// Add Federation directive definitions first
	sources = append(sources, &ast.Source{
		Name:  "federation.graphql",
		Input: federationDirectives,
	})

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("failed to read schema file %s: %w", file, err)
		}

		sources = append(sources, &ast.Source{
			Name:  filepath.Base(file),
			Input: string(content),
		})

		if verbose {
			fmt.Printf("Loaded schema file: %s\n", filepath.Base(file))
		}
	}

	// Parse the schema
	schema, err := gqlparser.LoadSchema(sources...)
	if err != nil {
		return nil, fmt.Errorf("failed to parse schema: %w", err)
	}

	return schema, nil
}

// federationDirectives contains the Apollo Federation directive definitions
const federationDirectives = `
directive @key(fields: String!, resolvable: Boolean = true) repeatable on OBJECT | INTERFACE
directive @requires(fields: String!) on FIELD_DEFINITION
directive @provides(fields: String!) on FIELD_DEFINITION
directive @external on FIELD_DEFINITION | OBJECT
directive @extends on OBJECT | INTERFACE
directive @shareable on OBJECT | FIELD_DEFINITION
directive @tag(name: String!) repeatable on FIELD_DEFINITION | OBJECT | INTERFACE | UNION | ARGUMENT_DEFINITION | SCALAR | ENUM | ENUM_VALUE | INPUT_OBJECT | INPUT_FIELD_DEFINITION
directive @override(from: String!) on FIELD_DEFINITION
directive @inaccessible on FIELD_DEFINITION | OBJECT | INTERFACE | UNION | ARGUMENT_DEFINITION | SCALAR | ENUM | ENUM_VALUE | INPUT_OBJECT | INPUT_FIELD_DEFINITION
directive @composeDirective(name: String!) repeatable on SCHEMA
directive @interfaceObject on OBJECT

scalar _Any
scalar _FieldSet
`

// isIntrospectionField returns true if the field is a GraphQL introspection field
func isIntrospectionField(fieldName string) bool {
	return strings.HasPrefix(fieldName, "__")
}

// isConnectionType returns true if the type name ends with "Connection"
func isConnectionType(typeName string) bool {
	return strings.HasSuffix(typeName, "Connection")
}

// getNamedType recursively unwraps a type to get the underlying named type
func getNamedType(t *ast.Type) string {
	if t == nil {
		return ""
	}
	return t.Name()
}

// capitalize returns the string with the first letter capitalized
func capitalize(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// normalize returns the string with the first letter capitalized and prefixed
// with "Get" If the string already starts with "get" (case-insensitive), it
// just capitalizes it without adding another "Get"
func normalize(s string) string {
	if s == "" {
		return ""
	}

	// Check if the string already starts with "get" (case-insensitive)
	if len(s) >= 3 && strings.ToLower(s[:3]) == "get" {
		// Already has "get" prefix, just capitalize the first letter
		return strings.ToUpper(s[:1]) + s[1:]
	}

	// Add "Get" prefix and capitalize the field name
	return "Get" + capitalize(s)
}
