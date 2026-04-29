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

// Inbound holds the schema definition for the Inbound entity.
type Inbound struct {
	ent.Schema
}

func (Inbound) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entgql.Type("ReceivingInbound"),
		entsql.Schema("receiving"),
		entsql.Table("inbounds"),
		entgql.RelayConnection(),
		entgql.Directives(importexport.Importable("",
			importexport.WithList("receivingInbounds"),
			importexport.WithCreate("createReceivingInbound"),
		)),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

// Fields of the Inbound.
func (Inbound) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.String("order_id").
			Optional().
			Annotations(
				entgql.OrderField("ORDER_ID"),
			),
		field.UUID("supplier_id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Annotations(
				entgql.OrderField("SUPPLIER_ID"),
			),
	}
}

func (Inbound) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("inboundItems", InboundItem.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("inboundItems"),
			),
		edge.To("inboundShipmentNotifications", InboundShipmentNotification.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("inboundShipmentNotifications"),
			),
	}
}

func (Inbound) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
