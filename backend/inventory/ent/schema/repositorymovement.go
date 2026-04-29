package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/importexport"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

// RepositoryMovement holds the schema definition for the RepositoryMovement entity.
type RepositoryMovement struct {
	ent.Schema
}

func (RepositoryMovement) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Schema("inventory"),
		entsql.Table("repository_movements"),
		entgql.RelayConnection(),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
		entgql.Directives(importexport.Importable("",
			importexport.WithList("repositoryMovements"),
			importexport.WithCreate("createInventoryRepositoryMovement"),
		)),
	}
}

// Fields of the RepositoryMovement.
func (RepositoryMovement) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.UUID("repository_id", uuid.UUID{}).
			Annotations(
				entgql.OrderField("REPOSITORY_ID"),
				entgql.Skip(entgql.SkipMutationUpdateInput),
			),
		field.UUID("from_id", uuid.UUID{}).
			Annotations(
				entgql.OrderField("FROM_ID"),
				entgql.Skip(entgql.SkipMutationUpdateInput),
			).
			Optional(),
		field.UUID("to_id", uuid.UUID{}).
			Annotations(
				entgql.OrderField("TO_ID"),
				entgql.Skip(entgql.SkipMutationUpdateInput),
			),
		field.Bool("executed").
			Default(false).
			Annotations(
				entgql.OrderField("EXECUTED"),
				entgql.Skip(entgql.SkipMutationUpdateInput),
			),
		field.Time("executed_at").
			Optional().
			Nillable(). // TODO(michael): remove .Nillable()
			Annotations(
				entgql.OrderField("EXECUTED_AT"),
				entgql.Skip(entgql.SkipMutationCreateInput, entgql.SkipMutationUpdateInput),
			),
		field.String("handler").
			NotEmpty().
			Annotations(entgql.OrderField("HANDLER")),
		field.Enum("blocked_by").
			Optional().
			Nillable().
			Values("RecalledProducts", "ExpiredProducts", "MislabelledGoods", "RegulatoryHold", "AwaitingDocumentation", "InventoryDiscrepancies", "HazardousMaterials", "CounterfeitGoods", "SeasonalGoods").
			Annotations(
				entgql.OrderField("BLOCKED_BY"),
			),
		field.UUID("collection_id", uuid.UUID{}).
			Optional().
			Immutable().
			Annotations(
				entgql.OrderField("COLLECTION_ID"),
				entgql.Skip(entgql.SkipMutationUpdateInput),
			),
		field.UUID("order_id", uuid.UUID{}).
			Optional().
			Annotations(
				entgql.OrderField("ORDER_ID"),
				entgql.Skip(entgql.SkipMutationUpdateInput),
			),
		field.Int("position").
			Default(0).
			Annotations(
				entgql.OrderField("POSITION"),
				entgql.Skip(entgql.SkipMutationUpdateInput),
			),
	}
}

// Edges of the RepositoryMovement.
func (RepositoryMovement) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("from", Repository.Type).
			Ref("repositoryMovementFromRepositories").
			Field("from_id").
			Unique().
			Annotations(
				entgql.Skip(entgql.SkipMutationUpdateInput),
			),
		edge.From("to", Repository.Type).
			Ref("repositoryMovementToRepositories").
			Field("to_id").
			Required().
			Unique().
			Annotations(
				entgql.Skip(entgql.SkipMutationUpdateInput),
			),
		edge.From("repository", Repository.Type).
			Ref("repositoryMovementRepositories").
			Field("repository_id").
			Required().
			Unique().
			Annotations(
				entgql.Skip(entgql.SkipMutationUpdateInput),
			),
		// edge.From("collection", Collection_Movement.Type).
		//	Ref("collectionMovementRepositoryMovement").
		//	Field("collection_id").
		//	Required().
		//	Unique(),
	}
}

func (RepositoryMovement) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
