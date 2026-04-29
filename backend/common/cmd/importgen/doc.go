/*
Package main implements importgen, a code generator that produces
import/export registry code from @pyckImportable GraphQL directives and the
typed API client interface.

# Usage

Run from a service directory (e.g. backend/inventory):

	go tool importgen [flags]

	-v, --verbose   Log detected entities and matched methods
	    --dry-run   Preview without writing files

Service name and module path are auto-detected via go list.
Schema directory (./graph), client path (./api/internal/client_gen.go),
and output directory (./api) are hardcoded by convention.

# How it works

importgen reads the GraphQL schema (ent.graphql) to find types annotated
with @pyckImportable, then parses the generated API client interface
(api/internal/client_gen.go) to match each entity to its List, Create, and
Update methods. The output is api/import_gen.go containing typed closures
that the import/export library dispatches to at runtime.

# Generation chain

importgen runs after apigenc in the go:generate sequence:

	entc       → ent/gen/*.go + graph/ent.graphql (with @pyckImportable on types)
	gqlgen     → gqlgen_gen.go
	apigen     → api/graph/apigen_gen.graphql
	gqlgenc    → api/internal/client_gen.go
	apigenc    → api/client_gen.go + api/models_gen.go
	importgen  → api/import_gen.go                    ← this tool

# Adding a new importable entity

Add the annotation to the Ent schema and run go generate:

	func (Repository) Annotations() []schema.Annotation {
	    return []schema.Annotation{
	        entgql.Directives(importexport.Importable("name")),
	        // ...
	    }
	}

importgen will automatically detect the new entity and generate its
registry entry. No other files need to be modified.

# Package layout

	importgen/
	  doc.go                         package documentation
	  main.go                        thin CLI entry point
	  api/
	    api.go                       public API: ParseImportableEntities, MatchEntity,
	                                 DetectServiceInfo
	    api_test.go                  tests for schema parsing
	  internal/
	    client.go                    ParseClientMethods, DetectAccessorChain
	    matcher.go                   Capitalize, DeriveInputType
	    renderer.go                  WriteRegistryFile + embedded template
	    templates/
	      import_gen.go.tmpl         output template
	  types/
	    types.go                     shared types (ImportExportEntry, RegistryEntity, etc.)
	    errors.go                    sentinel errors

# Output structure

	<service>/api/
	  import_gen.go     generated import/export entity registrations

The generated file exports a single function:

	func RegisterEntities(r *importexport.Registry, c Client) error

Each entity gets a registration function (e.g. registerLocation) that
creates an EntityDescriptor with typed List, Create, and Update closures.

# Method matching

For each @pyckImportable entity, the directive specifies the exact GraphQL
operation names. importgen capitalizes them to derive Go client method names:

	list: "repositories"                  → GetRepositories
	create: "createInventoryRepository"   → CreateInventoryRepository
	update: "updateInventoryRepository"   → UpdateInventoryRepository

The service prefix (e.g., "Inventory" for the inventory service) is tried
when the plain name doesn't match. Plural forms are generated automatically
(Repository → Repositories, Item → Items).

# Accessor chain detection

GraphQL mutations return nested response types. importgen parses the
internal client structs to determine the correct accessor chain:

	1-level: resp.GetCreateDataType()                              → entity has ID directly
	2-level: resp.GetCreateInventoryRepository().GetRepository()   → entity is wrapped

Detection is automatic by inspecting the generated response struct fields.

# Model import detection

When a create mutation uses a handwritten input type (e.g.
CreateReplenishmentOrderWithItemsInput) instead of an Ent-generated one,
the type lives in the service's model/ package rather than being re-exported
through api/. importgen detects the "model." prefix on input types and adds
the model import to the generated registry file automatically.
*/
package main
