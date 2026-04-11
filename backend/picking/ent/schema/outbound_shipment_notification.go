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

// OutboundShipmentNotification holds the schema definition for the OutboundShipmentNotification entity.
type OutboundShipmentNotification struct {
	ent.Schema
}

func (OutboundShipmentNotification) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entgql.Type("PickingOutboundShipmentNotification"),
		entsql.Schema("picking"),
		entsql.Table("outbound-shipment-notifications"),
		entgql.RelayConnection(),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

// Fields of the OutboundShipmentNotification.
func (OutboundShipmentNotification) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.UUID("order_id", uuid.UUID{}).
			Annotations(
				entgql.OrderField("ORDER_ID"),
			),
	}
}

// Edges of the OutboundShipmentNotification.
func (OutboundShipmentNotification) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("order", Order.Type).
			Ref("outboundShipmentNotifications").
			Field("order_id").
			Required().
			Unique(),
	}
}

func (OutboundShipmentNotification) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
