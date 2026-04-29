// Package types contains shared data types for importgen.
package types

type (
	// ImportExportEntry describes one pyckImportable entity parsed from the GraphQL schema.
	ImportExportEntry struct {
		TypeName       string
		IdentityField  string
		ListField      string // GraphQL query field name (e.g., "repositories")
		CreateMutation string // GraphQL mutation name (e.g., "createInventoryRepository")
		UpdateMutation string // GraphQL mutation name (e.g., "updateInventoryRepository")
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
		IdentityField       string
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
