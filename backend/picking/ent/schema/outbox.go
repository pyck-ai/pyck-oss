package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

// EntityEventsOutbox holds the schema definition for the entity events outbox table.
// This table is used by the Transactional Outbox Pattern to ensure
// reliable event delivery to NATS.
//
// Events are written to this table within the same transaction as entity
// mutations, then processed asynchronously by the OutboxHandler.
type EntityEventsOutbox struct {
	ent.Schema
}

func (EntityEventsOutbox) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Schema("picking"),
		entsql.Table("event_outbox"),
	}
}

func (EntityEventsOutbox) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.OutboxMixin{},
	}
}
