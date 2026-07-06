package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"

	_ "embed"
)

//go:embed templates/graphql.tmpl
var graphqlTemplate string

// TemplateData holds the data for template execution
type TemplateData struct {
	GeneratedAt string
	Queries     []OperationData
	Mutations   []OperationData
}

// OperationData holds data for a single GraphQL operation (query or mutation)
type OperationData struct {
	Name              string
	FieldName         string
	Params            []string
	Args              []string
	NeedsSelectionSet bool
	SelectionSet      string
}

// generateClientQueries generates client GraphQL query files from the schema using templates
func generateClientQueries(schema *ast.Schema, outputDir, operationsDir string) error {
	// Ensure output directory exists
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	data := TemplateData{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Queries:     []OperationData{},
		Mutations:   []OperationData{},
	}

	// Extract and generate queries
	if schema.Query != nil {
		for _, field := range schema.Query.Fields {
			if isIntrospectionField(field.Name) {
				if verbose {
					fmt.Printf("Skipping introspection query: %s\n", field.Name)
				}
				continue
			}

			logVerbosef("Generating query: %s", field.Name)

			opData, err := buildOperationData(schema, field, "query")
			if err != nil {
				// Skip queries that can't be generated (e.g., interface/union types)
				logVerbosef("Skipped query %s: %v", field.Name, err)
				continue
			}

			data.Queries = append(data.Queries, opData)
		}
	}

	// Extract and generate mutations
	if schema.Mutation != nil {
		for _, field := range schema.Mutation.Fields {
			if isIntrospectionField(field.Name) {
				if verbose {
					fmt.Printf("Skipping introspection mutation: %s\n", field.Name)
				}
				continue
			}

			logVerbosef("Generating mutation: %s", field.Name)

			opData, err := buildOperationData(schema, field, "mutation")
			if err != nil {
				logVerbosef("Skipped mutation %s: %v", field.Name, err)
				continue
			}

			data.Mutations = append(data.Mutations, opData)
		}
	}

	// Execute template
	tmpl, err := template.New("graphql").Parse(graphqlTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	// Write to file
	filename := filepath.Join(outputDir, "apigen_gen.graphql")
	if err := os.WriteFile(filename, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("failed to write GraphQL file %s: %w", filename, err)
	}

	logVerbosef("Generated: %s", filename)

	// Emit hand-written operations to a separate file. apigen only auto-generates
	// root-field operations with scalar selections (entity edges are intentionally
	// skipped), so federated cross-service relations — e.g. PickingOrder.customer
	// resolved by main-data — have no auto-generated operation. These are written
	// outside the apigen_gen.graphql / api/graph glob so the gqlgenc/apigenc
	// clients (validated against the local subgraph schema) never see them; only
	// brunogen, which runs operations through the federated gateway, picks them up.
	if err := writeHandWrittenOperations(operationsDir, outputDir); err != nil {
		return fmt.Errorf("failed to write hand-written operations: %w", err)
	}

	return nil
}

// handWrittenOperationsFile is the name of the generated file holding
// hand-written operations. It lives one level above the apigen output
// directory (i.e. api/, not api/graph/) so it is excluded from the
// api/graph/*.graphql globs used by the gqlgenc/apigenc clients.
const handWrittenOperationsFile = "operations_gen.graphql"

// writeHandWrittenOperations collects syntax-checked hand-written GraphQL
// operations from operationsDir (sorted by filename) and writes them to a single
// generated file alongside api/graph. A missing or empty directory removes any
// stale generated file and is otherwise a no-op, so services without
// hand-written operations are unaffected. Each file is parsed as a query
// document to catch syntax errors early; the raw source is then concatenated
// verbatim so brunogen reads it like any other operation.
func writeHandWrittenOperations(operationsDir, outputDir string) error {
	target := filepath.Join(filepath.Dir(outputDir), handWrittenOperationsFile)

	files, err := filepath.Glob(filepath.Join(operationsDir, "*.graphql"))
	if err != nil {
		return fmt.Errorf("failed to find hand-written operation files: %w", err)
	}
	if len(files) == 0 {
		// Drop a stale file if hand-written operations were all removed.
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove stale %s: %w", target, err)
		}
		return nil
	}
	sort.Strings(files)

	var buf bytes.Buffer
	buf.WriteString("# Code generated by github.com/pyck-ai/pyck/backend/common/cmd/apigen. DO NOT EDIT.\n")
	fmt.Fprintf(&buf, "# Hand-written operations, source: %s/*.graphql (edit there).\n", operationsDir)
	buf.WriteString("# These run through the federated gateway (e.g. cross-service relations)\n")
	buf.WriteString("# and are consumed by brunogen only, not the gqlgenc/apigenc clients.\n")

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("failed to read hand-written operation %s: %w", file, err)
		}

		if _, err := parser.ParseQuery(&ast.Source{Name: filepath.Base(file), Input: string(content)}); err != nil {
			return fmt.Errorf("invalid hand-written operation %s: %w", file, err)
		}

		logVerbosef("Including hand-written operations from: %s", file)
		buf.WriteString("\n")
		buf.Write(bytes.TrimSpace(content))
		buf.WriteString("\n")
	}

	if err := os.WriteFile(target, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", target, err)
	}
	logVerbosef("Generated: %s", target)

	return nil
}

// buildOperationData builds operation data for a query or mutation
func buildOperationData(schema *ast.Schema, field *ast.FieldDefinition, opType string) (OperationData, error) {
	var opData OperationData

	// Get the return type
	returnTypeName := getNamedType(field.Type)
	returnType := schema.Types[returnTypeName]

	// Skip interface and union types as they require fragments and type-specific handling
	if returnType != nil && (returnType.Kind == ast.Interface || returnType.Kind == ast.Union) {
		logVerbosef("Skipping %s %s: returns %s type %s", opType, field.Name, returnType.Kind, returnTypeName)
		return opData, ErrSkipInterfaceUnion
	}

	// Set operation name
	if opType == "query" {
		opData.Name = normalize(field.Name)
	} else {
		opData.Name = capitalize(field.Name)
	}

	opData.FieldName = field.Name
	opData.Params = buildParameters(field)
	opData.Args = buildArguments(field)

	// Check if the return type requires a selection set
	// Scalars and enums don't need selection sets
	opData.NeedsSelectionSet = false
	if returnType != nil && returnType.Kind == ast.Object {
		opData.NeedsSelectionSet = true
	} else if isConnectionType(returnTypeName) {
		opData.NeedsSelectionSet = true
	}

	if opData.NeedsSelectionSet {
		// Generate response fields
		if isConnectionType(returnTypeName) {
			// Generate paginated response structure
			opData.SelectionSet = generateConnectionFields(schema, returnTypeName)
		} else {
			// Generate regular response structure
			opData.SelectionSet = generateTypeFields(schema, returnTypeName, "    ")
		}
	}

	return opData, nil
}

// buildParameters builds the parameter list for a query/mutation
func buildParameters(field *ast.FieldDefinition) []string {
	if len(field.Arguments) == 0 {
		return nil
	}
	params := make([]string, 0, len(field.Arguments))
	for _, arg := range field.Arguments {
		param := fmt.Sprintf("$%s: %s", arg.Name, arg.Type.String())
		params = append(params, param)
	}
	return params
}

// buildArguments builds the argument list for a query/mutation
func buildArguments(field *ast.FieldDefinition) []string {
	if len(field.Arguments) == 0 {
		return nil
	}
	args := make([]string, 0, len(field.Arguments))
	for _, arg := range field.Arguments {
		args = append(args, fmt.Sprintf("%s: $%s", arg.Name, arg.Name))
	}
	return args
}

// generateConnectionFields generates the response structure for a Connection type
func generateConnectionFields(schema *ast.Schema, connectionTypeName string) string {
	var sb strings.Builder

	// Get the connection type definition
	connectionType := schema.Types[connectionTypeName]
	if connectionType == nil {
		return ""
	}

	// Add totalCount if it exists
	if connectionType.Fields.ForName("totalCount") != nil {
		sb.WriteString("    totalCount\n")
	}

	// Add pageInfo if it exists
	if connectionType.Fields.ForName("pageInfo") != nil {
		sb.WriteString("    pageInfo {\n")
		sb.WriteString("      hasNextPage\n")
		sb.WriteString("      hasPreviousPage\n")
		sb.WriteString("      startCursor\n")
		sb.WriteString("      endCursor\n")
		sb.WriteString("    }\n")
	}

	// Add edges if it exists
	if connectionType.Fields.ForName("edges") != nil {
		sb.WriteString("    edges {\n")
		sb.WriteString("      cursor\n")
		sb.WriteString("      node {\n")

		// Get the node type from the connection - only select scalar/enum fields
		nodeTypeName := strings.TrimSuffix(connectionTypeName, "Connection")
		sb.WriteString(generateTypeFields(schema, nodeTypeName, "        "))

		sb.WriteString("      }\n")
		sb.WriteString("    }\n")
	}

	return sb.String()
}

// generateTypeFields generates the field selection for a given type
func generateTypeFields(schema *ast.Schema, typeName, indent string) string {
	return generateTypeFieldsWithDepth(schema, typeName, indent, 0, make(map[string]bool))
}

// generateTypeFieldsWithDepth generates the field selection for a given type with depth tracking
func generateTypeFieldsWithDepth(schema *ast.Schema, typeName, indent string, depth int, visited map[string]bool) string { //nolint:unparam // visited is passed for potential cycle detection in recursive calls
	const maxDepth = 3 // Limit nesting depth for system types

	var sb strings.Builder

	// Find the type definition
	typeDef := schema.Types[typeName]
	if typeDef == nil {
		if verbose {
			fmt.Printf("Type not found: %s\n", typeName)
		}
		return ""
	}

	// Only process object types
	if typeDef.Kind != ast.Object {
		return ""
	}

	// Check if this is an entity type (implements Node interface)
	isEntity := implementsNode(typeDef)

	// Get all fields and sort them for consistent output
	fieldNames := make([]string, 0, len(typeDef.Fields))
	for _, field := range typeDef.Fields {
		// Skip introspection fields
		if isIntrospectionField(field.Name) {
			continue
		}
		fieldNames = append(fieldNames, field.Name)
	}
	sort.Strings(fieldNames)

	// Generate field selections
	for _, fieldName := range fieldNames {
		field := typeDef.Fields.ForName(fieldName)
		if field == nil {
			continue
		}

		// Get the field type
		fieldTypeName := getNamedType(field.Type)
		fieldTypeDef := schema.Types[fieldTypeName]

		// Check if it's a simple type (scalar/enum)
		isSimpleType := fieldTypeDef == nil || fieldTypeDef.Kind == ast.Scalar || fieldTypeDef.Kind == ast.Enum

		if isSimpleType {
			// Always include scalar and enum fields
			sb.WriteString(fmt.Sprintf("%s%s\n", indent, field.Name))
		} else if fieldTypeDef.Kind == ast.Object {
			// For object types we never auto-expand reverse relations in a
			// node's default selection:
			//   - direct entity→entity edges (e.g. repository.parent), and
			//   - paginated Connection fields (e.g.
			//     repository.itemMovementFromRepositories).
			// Both are unbounded: expanding a Connection pulls a node's entire
			// related history (every movement, every child), which bloated
			// generated responses to tens of MB. A caller that needs a relation
			// issues an explicit, filtered+paginated query against the
			// relation's own root field (e.g. GetItemMovements with a where on
			// the repository); the schema still exposes the nested shape for
			// hand-written operations.
			fieldIsEntity := implementsNode(fieldTypeDef)

			if (isEntity && fieldIsEntity) || isConnectionType(fieldTypeName) {
				continue
			}

			// Otherwise it's a bounded embedded/system object — expand it.
			if depth < maxDepth {
				sb.WriteString(fmt.Sprintf("%s%s {\n", indent, field.Name))
				nestedIndent := indent + "  "
				nestedFields := generateTypeFieldsWithDepth(schema, fieldTypeName, nestedIndent, depth+1, visited)
				sb.WriteString(nestedFields)
				sb.WriteString(fmt.Sprintf("%s}\n", indent))
			}
		}
	}

	return sb.String()
}

// implementsNode checks if a type implements the Node interface
func implementsNode(typeDef *ast.Definition) bool {
	if typeDef == nil || typeDef.Kind != ast.Object {
		return false
	}
	for _, iface := range typeDef.Interfaces {
		if iface == "Node" {
			return true
		}
	}
	return false
}
