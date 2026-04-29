/*
Package api is the public API for importgen.

It provides functions to parse @pyckImportable entities from a GraphQL schema,
resolve them against an API client interface, and generate registry code.
*/
package api

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pyck-ai/pyck/backend/common/cmd/importgen/internal"
	"github.com/pyck-ai/pyck/backend/common/cmd/importgen/types"
)

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

// ParseImportableEntities scans the GraphQL schema directory for types with
// the @pyckImportable directive and returns the parsed entries.
func ParseImportableEntities(schemaDir string) ([]types.ImportExportEntry, error) {
	schema, err := loadSchema(schemaDir)
	if err != nil {
		return nil, err
	}

	var entries []types.ImportExportEntry

	for _, typeDef := range schema.Types {
		if typeDef.Kind != ast.Object {
			continue
		}

		for _, dir := range typeDef.Directives {
			if dir.Name != "pyckImportable" {
				continue
			}

			entry := types.ImportExportEntry{
				TypeName: typeDef.Name,
			}
			if identityField := dir.Arguments.ForName("identityField"); identityField != nil {
				entry.IdentityField = identityField.Value.Raw
			}
			if arg := dir.Arguments.ForName("list"); arg != nil {
				entry.ListField = arg.Value.Raw
			}
			if arg := dir.Arguments.ForName("create"); arg != nil {
				entry.CreateMutation = arg.Value.Raw
			}
			if arg := dir.Arguments.ForName("update"); arg != nil {
				entry.UpdateMutation = arg.Value.Raw
			}

			entries = append(entries, entry)
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].TypeName < entries[j].TypeName
	})

	return entries, nil
}

// MatchEntity matches an ImportExportEntry to API client methods and
// determines accessor chains. The entry must have ListField, CreateMutation,
// and UpdateMutation set from the @pyckImportable directive.
func MatchEntity(entry types.ImportExportEntry, methods map[string]types.ClientMethod, clientPath string) (types.RegistryEntity, error) {
	e := types.RegistryEntity{
		TypeName:      entry.TypeName,
		IdentityField: entry.IdentityField,
	}

	// Capitalize GraphQL names to get Go method names.
	e.ListMethod = internal.Capitalize("get" + internal.Capitalize(entry.ListField))
	e.CreateMethod = internal.Capitalize(entry.CreateMutation)

	// Verify required methods exist in the client interface.
	if _, ok := methods[e.ListMethod]; !ok {
		return e, fmt.Errorf("%w %q (from list: %q)", types.ErrMethodNotFound, e.ListMethod, entry.ListField)
	}
	if _, ok := methods[e.CreateMethod]; !ok {
		return e, fmt.Errorf("%w %q (from create: %q)", types.ErrMethodNotFound, e.CreateMethod, entry.CreateMutation)
	}

	e.ListArgsType = e.ListMethod + "Args"
	e.ListAccessor = e.ListMethod
	e.CreateArgsType = e.CreateMethod + "Args"
	e.CreateInputType = internal.DeriveInputType(methods, e.CreateMethod)
	e.WhereInputType = entry.TypeName + "WhereInput"

	// Detect create accessor chain.
	createChain, err := internal.DetectAccessorChain(clientPath, e.CreateMethod)
	if err != nil {
		return e, fmt.Errorf("detect create accessor: %w", err)
	}
	e.CreateAccessorChain = createChain

	// Update is optional — create-only entities omit WithUpdate().
	if entry.UpdateMutation != "" {
		e.UpdateMethod = internal.Capitalize(entry.UpdateMutation)
		if _, ok := methods[e.UpdateMethod]; !ok {
			return e, fmt.Errorf("%w %q (from update: %q)", types.ErrMethodNotFound, e.UpdateMethod, entry.UpdateMutation)
		}
		e.UpdateArgsType = e.UpdateMethod + "Args"
		e.UpdateInputType = internal.DeriveInputType(methods, e.UpdateMethod)

		updateChain, err := internal.DetectAccessorChain(clientPath, e.UpdateMethod)
		if err != nil {
			return e, fmt.Errorf("detect update accessor: %w", err)
		}
		e.UpdateAccessorChain = updateChain
	}

	return e, nil
}

// DetectServiceInfo uses `go list` to determine the current module path, then
// derives the service name and module base.
func DetectServiceInfo(ctx context.Context) (types.ServiceInfo, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-f", "{{.ImportPath}}")
	output, err := cmd.Output()
	if err != nil {
		return types.ServiceInfo{}, fmt.Errorf("go list: %w", err)
	}

	importPath := strings.TrimSpace(string(output))
	parts := strings.Split(importPath, "/")

	for i, part := range parts {
		if part == "backend" && i+1 < len(parts) {
			return types.ServiceInfo{
				ServiceName: parts[i+1],
				ModuleBase:  strings.Join(parts[:i+1], "/"),
			}, nil
		}
	}

	return types.ServiceInfo{}, fmt.Errorf("%w %q", types.ErrNoBackendSegment, importPath)
}

// HasModelPrefix returns true if the type name has a "model." package prefix.
func HasModelPrefix(typeName string) bool {
	return strings.HasPrefix(typeName, "model.")
}

func loadSchema(schemaDir string) (*ast.Schema, error) {
	files, err := filepath.Glob(filepath.Join(schemaDir, "*.graphql"))
	if err != nil {
		return nil, fmt.Errorf("find schema files: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w %q", types.ErrNoGraphQLFiles, schemaDir)
	}

	sources := make([]*ast.Source, 0, len(files)+1)
	sources = append(sources, &ast.Source{
		Name:  "federation.graphql",
		Input: federationDirectives,
	})

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		sources = append(sources, &ast.Source{
			Name:  filepath.Base(file),
			Input: string(content),
		})
	}

	schema, err := gqlparser.LoadSchema(sources...)
	if err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}

	return schema, nil
}
