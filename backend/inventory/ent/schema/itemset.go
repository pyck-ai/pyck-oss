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
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

// ItemSet holds the schema definition for the ItemSet entity.
type ItemSet struct {
	ent.Schema
}

func (ItemSet) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entgql.Type("InventoryItemSet"),
		entsql.Schema("inventory"),
		entsql.Table("item_sets"),
		entgql.RelayConnection(),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
		entgql.OrderField("sku"),
	}
}

// Fields of the ItemSet.
func (ItemSet) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.String("sku").
			NotEmpty().
			Annotations(
				entgql.OrderField("SKU"),
			),
	}
}

func (ItemSet) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "sku").
			Unique().
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()),
	}
}

// Edges of the ItemSet.
func (ItemSet) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("items", Item.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("items"),
			),
	}
}

func (ItemSet) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
