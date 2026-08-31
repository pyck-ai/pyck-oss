// Package types contains shared data types for importgen.
package types

type (
	// ImportExportEntry describes one pyckImportable entity parsed from the GraphQL schema.
	ImportExportEntry struct {
		TypeName       string
		IdentityFields []string    // identity fields (e.g., ["name"] or ["slug","version"])
		References     []Reference // declared outgoing FK edges
		ListField      string      // GraphQL query field name (e.g., "repositories")
		CreateMutation string      // GraphQL mutation name (e.g., "createInventoryRepository")
		UpdateMutation string      // GraphQL mutation name (e.g., "updateInventoryRepository")
	}

	// Reference is a declared FK edge parsed from a @pyckImportable references arg
	// entry of the form "field:TargetType".
	Reference struct {
		Field      string
		TargetType string
	}

	// ClientMethod represents a parsed method from the API client interface.
	ClientMethod struct {
		Name   string
		Params []MethodParam
	}

	// MethodParam represents a parameter of a client method.
	MethodParam struct {
		Name string
		Type string
	}

	// RegistryEntity holds all derived information for one entity's registry entry.
	RegistryEntity struct {
		TypeName            string
		IdentityFields      []string
		References          []Reference
		ListMethod          string
		CreateMethod        string
		UpdateMethod        string
		ListArgsType        string
		CreateArgsType      string
		UpdateArgsType      string
		CreateInputType     string
		UpdateInputType     string
		WhereInputType      string
		CreateAccessorChain string
		ImmutableFields     []string
		UpdateAccessorChain string
		ListAccessor        string
	}

	// TemplateData is passed to the import_gen.go.tmpl template.
	TemplateData struct {
		ServiceName     string
		ModelImportPath string
		Entities        []RegistryEntity
	}

	// ServiceInfo holds auto-detected service metadata.
	ServiceInfo struct {
		ServiceName string
		ModuleBase  string
	}
)
