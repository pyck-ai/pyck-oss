package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

// Collection_Movement holds the schema definition for the Collection_Movement entity.
type Collection_Movement struct {
	ent.Schema
}

func (Collection_Movement) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entgql.Type("InventoryCollection"),
		entsql.Schema("inventory"),
		entsql.Table("collection_movements"),
		entgql.RelayConnection(),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationUpdate()),
	}
}

// Fields of the CollectionMovement.
func (Collection_Movement) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.String("handler").
			Optional().
			Annotations(
				entgql.OrderField("HANDLER"),
			),
	}
}

// Edges of the CollectionMovement.
func (Collection_Movement) Edges() []ent.Edge {
	//return []ent.Edge{
	//	edge.To("collectionMovementItemMovement", ItemMovement.Type),
	//	edge.To("collectionMovementRepositoryMovement", RepositoryMovement.Type),
	//}
	return nil
}

func (Collection_Movement) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
