package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

// Repository holds the schema definition for the Repository entity.
type Repository struct {
	ent.Schema
}

func (Repository) Annotations() []schema.Annotation {
	keyDirective := entgql.NewDirective("key", &ast.Argument{
		Name: "fields",
		Value: &ast.Value{
			Raw:  "id",
			Kind: ast.StringValue,
		},
	})
	return []schema.Annotation{
		entsql.Schema("inventory"),
		entsql.Table("repositories"),
		entgql.RelayConnection(),
		entgql.Directives(keyDirective),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

// Fields of the Repository.
func (Repository) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.UUID("parent_id", uuid.UUID{}).
			Optional().
			Annotations(
				entgql.OrderField("PARENT_ID"),
			),
		field.UUID("location_id", uuid.UUID{}).
			Optional().
			Annotations(
				entgql.OrderField("LOCATION_ID"),
			),
		field.String("name").
			NotEmpty().
			Annotations(
				entgql.OrderField("NAME"),
			),
		field.String("layout").
			Optional().
			Annotations(
				entgql.OrderField("LAYOUT"),
			),
		field.Enum("type").
			Values("dynamic", "static").
			Annotations(
				entgql.OrderField("TYPE"),
			),
		field.Bool("virtual_repo").
			Default(false).
			Immutable().
			Annotations(
				entgql.OrderField("VIRTUAL_REPO"),
			),
	}
}

// Edges of the Repository.
func (Repository) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("itemMovementToRepositories", ItemMovement.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("itemMovementToRepositories"),
			),
		edge.To("itemMovementFromRepositories", ItemMovement.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("itemMovementFromRepositories"),
			),
		edge.To("repositoryMovementToRepositories", RepositoryMovement.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("repositoryMovementToRepositories"),
			),
		edge.To("repositoryMovementFromRepositories", RepositoryMovement.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("repositoryMovementFromRepositories"),
			),
		edge.To("repositoryMovementRepositories", RepositoryMovement.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("repositoryMovementRepositories"),
			),
		edge.To("repositoryTransactions", Transaction.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("repositoryTransactions"),
			),
		edge.To("repositoryStocks", Stock.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("repositoryStocks"),
			),
		edge.To("children", Repository.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("children"),
			).
			From("parent").
			Field("parent_id").
			Unique(),
	}
}

func (Repository) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "name").
			Unique().
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()),
	}
}

func (Repository) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
