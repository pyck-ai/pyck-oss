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

// InboundShipmentNotification holds the schema definition for the InboundShipmentNotification entity.
type InboundShipmentNotification struct {
	ent.Schema
}

func (InboundShipmentNotification) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entgql.Type("ReceivingInboundShipmentNotification"),
		entsql.Schema("receiving"),
		entsql.Table("inbound-shipment-notifications"),
		entgql.RelayConnection(),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

// Fields of the InboundShipmentNotification.
func (InboundShipmentNotification) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.UUID("inbound_id", uuid.UUID{}).
			Annotations(
				entgql.OrderField("INBOUND_ID"),
			),
	}
}

// Edges of the InboundShipmentNotification.
func (InboundShipmentNotification) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("inbound", Inbound.Type).
			Ref("inboundShipmentNotifications").
			Field("inbound_id").
			Required().
			Unique(),
	}
}

func (InboundShipmentNotification) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
