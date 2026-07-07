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
