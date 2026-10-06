package main

import (
	"strings"

	"entgo.io/contrib/entgql"
	ent "entgo.io/ent/entc/gen"
	"github.com/vektah/gqlparser/v2/ast"
)

// pyckImportableDirectiveSchemaHook adds the @importable directive definition to the
// generated GraphQL schema so it appears in ent.graphql alongside @goField and
// @goModel. Only emits the directive when at least one schema uses @importable,
// avoiding noise in services that don't use import/export.
func pyckImportableDirectiveSchemaHook(g *ent.Graph, s *ast.Schema) error {
	if !hasPyckImportableEntity(g) {
		return nil
	}
	s.Directives["pyckImportable"] = &ast.DirectiveDefinition{
		Name:        "pyckImportable",
		Description: "Marks an entity as importable/exportable via the generic import mechanism.",
		Locations:   []ast.DirectiveLocation{ast.LocationObject},
		Arguments: ast.ArgumentDefinitionList{
			{
				Name: "identityField",
				Type: ast.NonNullNamedType("String", nil),
			},
			{
				Name: "list",
				Type: ast.NonNullNamedType("String", nil),
			},
			{
				Name: "create",
				Type: ast.NonNullNamedType("String", nil),
			},
			{
				Name: "update",
				Type: ast.NamedType("String", nil),
			},
			{
				Name: "references",
				Type: ast.ListType(ast.NonNullNamedType("String", nil), nil),
			},
		},
		Position: &ast.Position{Src: &ast.Source{BuiltIn: false}},
	}
	return nil
}

// hasPyckImportableEntity checks if any schema node uses the @importable directive
// by inspecting entgql annotations on the graph nodes.
func hasPyckImportableEntity(g *ent.Graph) bool {
	for _, node := range g.Nodes {
		ant := &entgql.Annotation{}
		if raw, ok := node.Annotations[ant.Name()]; ok {
			if err := ant.Decode(raw); err == nil {
				for _, d := range ant.Directives {
					if d.Name == "pyckImportable" {
						return true
					}
				}
			}
		}
	}
	return false
}

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

// dataMixinGQLTypes returns the GraphQL type names of graph nodes that embed
// DataMixin, detected by the presence of the data_type_slug field (the same
// marker the set_input_with_datatype template uses). Respects an entgql Type
// annotation override.
func dataMixinGQLTypes(g *ent.Graph) map[string]bool {
	types := make(map[string]bool)
	for _, node := range g.Nodes {
		hasSlug := false
		for _, f := range node.Fields {
			if f.Name == "data_type_slug" {
				hasSlug = true
				break
			}
		}
		if !hasSlug {
			continue
		}
		name := node.Name
		ant := &entgql.Annotation{}
		if raw, ok := node.Annotations[ant.Name()]; ok {
			if err := ant.Decode(raw); err == nil && ant.Type != "" {
				name = ant.Type
			}
		}
		types[name] = true
	}
	return types
}

// dropClearDataTypeID removes the clearDataTypeID field from the
// Update<Type>Input definition of every given DataMixin type. data_type_slug
// is server-derived from data_type_id and hidden from mutation inputs, so a
// client-visible clear on the id alone would produce a row with a NULL
// data_type_id and a stale non-empty data_type_slug — the exact shape the
// #990 precondition guard migration refuses to boot on. Re-pinning is done
// by SETTING a new id; clearing has no product use, so the field is not
// exposed at all.
func dropClearDataTypeID(s *ast.Schema, dataMixinTypes map[string]bool) {
	for typeName := range dataMixinTypes {
		def, ok := s.Types["Update"+typeName+"Input"]
		if !ok || def.Kind != ast.InputObject {
			continue
		}
		fields := def.Fields[:0]
		for _, f := range def.Fields {
			if f.Name != "clearDataTypeID" {
				fields = append(fields, f)
			}
		}
		def.Fields = fields
	}
}

// dropClearDataTypeIDSchemaHook wires dropClearDataTypeID into the entgql
// schema generation pipeline.
func dropClearDataTypeIDSchemaHook(g *ent.Graph, s *ast.Schema) error {
	dropClearDataTypeID(s, dataMixinGQLTypes(g))
	return nil
}

// deprecatedWhereFields lists generated where-input fields to mark @deprecated,
// as where-input type name -> field name -> reason. entgql only deprecates
// fields and types, not the has<Edge> predicates it derives from an edge, so
// they are listed here. A type or field absent from the schema is skipped, so
// the entry is inert in every other service.
var deprecatedWhereFields = map[string]map[string]string{
	"InventoryItemWhereInput": {
		"hasItemStocks":     inventoryItemStocksDeprecation,
		"hasItemStocksWith": inventoryItemStocksDeprecation,
	},
}

const inventoryItemStocksDeprecation = "Matches any stock row, including superseded versions. Use hasCurrentStock / hasCurrentStockWith."

// deprecateWhereFields adds @deprecated(reason) to each listed where-input
// field that exists and is not deprecated yet.
func deprecateWhereFields(s *ast.Schema, fields map[string]map[string]string) {
	for typeName, byField := range fields {
		def, ok := s.Types[typeName]
		if !ok || def.Kind != ast.InputObject {
			continue
		}
		for fieldName, reason := range byField {
			f := def.Fields.ForName(fieldName)
			if f == nil || f.Directives.ForName("deprecated") != nil {
				continue
			}
			f.Directives = append(f.Directives, &ast.Directive{
				Name: "deprecated",
				Arguments: ast.ArgumentList{
					{Name: "reason", Value: &ast.Value{Kind: ast.StringValue, Raw: reason}},
				},
			})
		}
	}
}

// deprecateWhereFieldsSchemaHook wires deprecateWhereFields into the entgql
// schema generation pipeline.
func deprecateWhereFieldsSchemaHook(_ *ent.Graph, s *ast.Schema) error {
	deprecateWhereFields(s, deprecatedWhereFields)
	return nil
}
