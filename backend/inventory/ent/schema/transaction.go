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
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

// Transaction holds the schema definition for the Transaction entity.
type Transaction struct {
	ent.Schema
}

func (Transaction) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Schema("inventory"),
		entsql.Table("transactions"),
		entgql.RelayConnection(),
		entgql.QueryField(),
		entgql.OrderField("quantity"),
		entgql.OrderField("type"),
	}
}

// Fields of the Transaction.
func (Transaction) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.UUID("item_id", uuid.UUID{}).
			Annotations(
				entgql.OrderField("ITEM_ID"),
			),
		field.UUID("repository_id", uuid.UUID{}).
			Annotations(
				entgql.OrderField("REPOSITORY_ID"),
			),
		field.Int64("quantity").
			Min(0).
			Annotations(
				entgql.OrderField("QUANTITY"),
			),
		field.Enum("type").
			Values("into", "out").
			Annotations(
				entgql.OrderField("TYPE"),
			),
	}
}

// Edges of the Transaction.
func (Transaction) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("item", Item.Type).
			Ref("itemTransactions").
			Field("item_id").
			Required().
			Unique(),
		edge.From("repository", Repository.Type).
			Ref("repositoryTransactions").
			Field("repository_id").
			Required().
			Unique(),
	}
}

func (Transaction) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
