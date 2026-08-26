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
			Immutable().
			Annotations(
				entgql.OrderField("REPOSITORY_ID"),
			),
		field.UUID("from_id", uuid.UUID{}).
			Immutable().
			Annotations(
				entgql.OrderField("FROM_ID"),
			).
			Optional(),
		field.UUID("to_id", uuid.UUID{}).
			Immutable().
			Annotations(
				entgql.OrderField("TO_ID"),
			),
		// executed/executed_at stay SkipMutationUpdateInput rather than
		// Immutable: they are process-managed, flipped after creation when the
		// movement is executed (see inventory/service/stock executor). Hiding
		// them from the GraphQL update input still blocks client backdoors.
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
			),
		field.UUID("order_id", uuid.UUID{}).
			Optional().
			Immutable().
			Annotations(
				entgql.OrderField("ORDER_ID"),
			),
		field.Int("position").
			Default(0).
			Immutable().
			Annotations(
				entgql.OrderField("POSITION"),
			),
	}
}

// Edges of the RepositoryMovement.
func (RepositoryMovement) Edges() []ent.Edge {
	return []ent.Edge{
		// The from/to/repository edges are backed by Immutable fields, so Ent
		// already excludes them from the update input; no SkipMutationUpdateInput
		// needed.
		edge.From("from", Repository.Type).
			Ref("repositoryMovementFromRepositories").
			Field("from_id").
			Immutable().
			Unique(),
		edge.From("to", Repository.Type).
			Ref("repositoryMovementToRepositories").
			Field("to_id").
			Required().
			Immutable().
			Unique(),
		edge.From("repository", Repository.Type).
			Ref("repositoryMovementRepositories").
			Field("repository_id").
			Required().
			Immutable().
			Unique(),
		// edge.From("collection", Collection_Movement.Type).
		//	Ref("collectionMovementRepositoryMovement").
		//	Field("collection_id").
		//	Required().
		//	Unique(),
	}
}

func (RepositoryMovement) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("collection_id", "position"),
		index.Fields("from_id"),
		index.Fields("repository_id"),
		index.Fields("executed"),
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
