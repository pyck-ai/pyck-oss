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
	"github.com/vektah/gqlparser/v2/ast"
)

// ReplenishmentOrderItem holds the schema definition for the ReplenishmentOrderItem entity.
type ReplenishmentOrderItem struct {
	ent.Schema
}

func (ReplenishmentOrderItem) Annotations() []schema.Annotation {
	keyDirective := entgql.NewDirective("key", &ast.Argument{
		Name: "fields",
		Value: &ast.Value{
			Raw:  "id",
			Kind: ast.StringValue,
		},
	})
	return []schema.Annotation{
		entgql.Type("ReplenishmentOrderItem"),
		entsql.Schema("inventory"),
		entsql.Table("replenishment_order_items"),
		entgql.RelayConnection(),
		entgql.Directives(keyDirective),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

// Fields of the ReplenishmentOrderItem.
func (ReplenishmentOrderItem) Fields() []ent.Field {
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
		field.Int64("quantity").
			Min(0).
			Annotations(
				entgql.OrderField("QUANTITY"),
			),
		field.UUID("replenishment_order_id", uuid.UUID{}).
			Immutable().
			Annotations(
				entgql.OrderField("REPLENISHMENT_ORDER_ID"),
			),
	}
}

// Edges of the ReplenishmentOrderItem
func (ReplenishmentOrderItem) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("replenishmentOrder", ReplenishmentOrder.Type).
			Ref("replenishmentOrderItems").
			Field("replenishment_order_id").
			Required().
			Unique().
			Immutable(),
	}
}

func (ReplenishmentOrderItem) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
