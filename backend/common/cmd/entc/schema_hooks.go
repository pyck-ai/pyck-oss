package main

import (
	"strings"

	ent "entgo.io/ent/entc/gen"
	"github.com/vektah/gqlparser/v2/ast"
)

// jsonbOrderSchemaHook modifies all *Order input types in the generated GraphQL schema
// to support ordering by JSONB sub-fields. For each *Order type it:
//   - Adds jsonPath (String) and jsonType (JSONType enum) optional fields
//   - Makes the "field" field nullable (so clients can omit it when using JSONB ordering)
func jsonbOrderSchemaHook(_ *ent.Graph, s *ast.Schema) error {
	// Define JSONType enum once.
	s.Types["JSONType"] = &ast.Definition{
		Kind:        ast.Enum,
		Name:        "JSONType",
		Description: "JSON type for casting extracted JSONB values during ordering.",
		EnumValues: ast.EnumValueList{
			{Name: "NUMBER", Description: "Cast to numeric for number comparisons."},
			{Name: "STRING", Description: "Cast to text for string comparisons."},
			{Name: "BOOLEAN", Description: "Cast to boolean for boolean comparisons."},
		},
	}

	for name, def := range s.Types {
		if def.Kind != ast.InputObject || !strings.HasSuffix(name, "Order") {
			continue
		}

		fieldDef := def.Fields.ForName("field")
		if fieldDef == nil {
			continue
		}

		// Make "field" nullable (remove NonNull) so clients can omit it when using JSONB ordering.
		fieldDef.Type.NonNull = false

		// Add JSONB ordering fields.
		def.Fields = append(def.Fields,
			&ast.FieldDefinition{
				Name:        "jsonPath",
				Description: "Dot-notation path into a JSONB column (e.g. \"meta.weight\").",
				Type:        ast.NamedType("String", nil),
			},
			&ast.FieldDefinition{
				Name:        "jsonType",
				Description: "Cast type for the extracted JSONB value.",
				Type:        ast.NamedType("JSONType", nil),
			},
		)
	}

	return nil
}
