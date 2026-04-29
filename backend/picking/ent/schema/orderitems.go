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
	"github.com/vektah/gqlparser/v2/ast"
)

// OrderItems holds the schema definition for the OrderItems entity.
type OrderItems struct {
	ent.Schema
}

func (OrderItems) Annotations() []schema.Annotation {
	keyDirective := entgql.NewDirective("key", &ast.Argument{
		Name: "fields",
		Value: &ast.Value{
			Raw:  "sku",
			Kind: ast.StringValue,
		},
	})
	return []schema.Annotation{
		entgql.Type("PickingOrderItem"),
		entsql.Schema("picking"),
		entsql.Table("order-items"),
		entgql.RelayConnection(),
		entgql.Directives(keyDirective, importexport.Importable("",
			importexport.WithList("pickingOrderItems"),
			importexport.WithCreate("createPickingOrderItem"),
		)),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

// Fields of the OrderItems.
func (OrderItems) Fields() []ent.Field {
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
		field.UUID("order_id", uuid.UUID{}).
			Annotations(
				entgql.OrderField("ORDER_ID"),
			),
		field.Int64("quantity").
			Min(0).
			Annotations(
				entgql.OrderField("QUANTITY"),
			),
	}
}

// Edges of the OrderItems.
func (OrderItems) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("order", Order.Type).
			Ref("orderItems").
			Field("order_id").
			Required().
			Unique(),
	}
}

func (OrderItems) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
