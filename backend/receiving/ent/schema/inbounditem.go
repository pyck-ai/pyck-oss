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

// InboundItem holds the schema definition for the InboundItem entity.
type InboundItem struct {
	ent.Schema
}

func (InboundItem) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entgql.Type("ReceivingInboundItem"),
		entsql.Schema("receiving"),
		entsql.Table("inbound-items"),
		entgql.RelayConnection(),
		entgql.Directives(importexport.Importable("",
			importexport.WithList("receivingInboundItems"),
			importexport.WithCreate("createReceivingInboundItem"),
		)),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

// Fields of the InboundItem.
func (InboundItem) Fields() []ent.Field {
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
		field.UUID("inbound_id", uuid.UUID{}).
			Annotations(
				entgql.OrderField("INBOUND_ID"),
			),
		field.Int64("quantity").
			Min(0).
			Annotations(
				entgql.OrderField("QUANTITY"),
			),
	}
}

// Edges of the InboundItem.
func (InboundItem) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("inbound", Inbound.Type).
			Ref("inboundItems").
			Field("inbound_id").
			Required().
			Unique(),
	}
}

func (InboundItem) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
