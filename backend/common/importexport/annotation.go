package importexport

import (
	"entgo.io/contrib/entgql"
	"github.com/vektah/gqlparser/v2/ast"
)

// Option configures the @pyckImportable directive.
type Option func(*options)

type options struct {
	list, create, update string
}

// WithList sets the GraphQL query field name for listing entities
// (e.g., "repositories", "locations").
func WithList(name string) Option { return func(o *options) { o.list = name } }

// WithCreate sets the GraphQL mutation name for creating entities
// (e.g., "createInventoryRepository", "createLocation").
func WithCreate(name string) Option { return func(o *options) { o.create = name } }

// WithUpdate sets the GraphQL mutation name for updating entities
// (e.g., "updateInventoryRepository", "updateLocation").
func WithUpdate(name string) Option { return func(o *options) { o.update = name } }

// Importable returns an entgql Directive that marks an entity as
// importable/exportable.
//
// The identityField is the field used for existence checks during import
// (e.g., "name", "slug", "sku"). It must uniquely identify an entity within
// a tenant.
//
// The list, create, and update options specify the exact GraphQL operation
// names used by the generated import/export registry.
//
// Usage in an Ent schema:
//
//	func (Repository) Annotations() []schema.Annotation {
//	    return []schema.Annotation{
//	        entgql.Directives(importexport.Importable("name",
//	            importexport.WithList("repositories"),
//	            importexport.WithCreate("createInventoryRepository"),
//	            importexport.WithUpdate("updateInventoryRepository"),
//	        )),
//	    }
//	}
func Importable(identityField string, opts ...Option) entgql.Directive {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	args := []*ast.Argument{
		stringArg("identityField", identityField),
		stringArg("list", o.list),
		stringArg("create", o.create),
		stringArg("update", o.update),
	}

	return entgql.NewDirective("pyckImportable", args...)
}

func stringArg(name, value string) *ast.Argument {
	return &ast.Argument{
		Name: name,
		Value: &ast.Value{
			Raw:  value,
			Kind: ast.StringValue,
		},
	}
}
