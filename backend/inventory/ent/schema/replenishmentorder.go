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

// ReplenishmentOrder holds the schema definition for the ReplenishmentOrder entity.
type ReplenishmentOrder struct {
	ent.Schema
}

func (ReplenishmentOrder) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entgql.Type("ReplenishmentOrder"),
		entsql.Schema("inventory"),
		entsql.Table("replenishment_orders"),
		entgql.RelayConnection(),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
		entgql.Directives(importexport.Importable("",
			importexport.WithList("replenishmentOrders"),
			importexport.WithCreate("createReplenishmentOrder"),
		)),
	}
}

// Fields of the ReplenishmentOrder.
func (ReplenishmentOrder) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.UUID("supplier_id", uuid.UUID{}).
			Optional().
			Annotations(
				entgql.OrderField("SUPPLIER_ID"),
			),
	}
}

// Edges of the ReplenishmentOrder
func (ReplenishmentOrder) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("replenishmentOrderItems", ReplenishmentOrderItem.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("replenishmentOrderItems"),
			),
	}
}

func (ReplenishmentOrder) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
