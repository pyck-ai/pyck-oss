package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

// Event is the legacy event registry entity (table "management.events").
//
// Deprecated: the event registry is no longer maintained by this service. The
// EventRegistryService that populated this table (and loaded it into memory at
// startup) has been removed; the data is being moved to an external store that
// siphons events directly from NATS like every other consumer. This schema is
// retained ONLY so the migration generator does not drop the existing table and
// its data. Do not add new usages. Scheduled for removal once the external
// store is in place and the table can be migrated/dropped.
type Event struct {
	ent.Schema
}

func (Event) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Schema("management"),
		entsql.Table("events"),
		entgql.RelayConnection(),
		entgql.QueryField(),
	}
}

func (Event) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.String("topic").
			Unique().
			Immutable().
			Annotations(
				entgql.OrderField("TOPIC"),
			),
		field.String("name").
			Default("").
			Annotations(
				entgql.OrderField("NAME"),
			),
		field.String("description").
			Default("").
			Annotations(
				entgql.OrderField("DESCRIPTION"),
			),
		field.JSON("example", map[string]any{}).
			Optional().
			Annotations(
				entgql.Type("Map"),
			),
	}
}

func (Event) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.HistoryMixin{},
	}
}
