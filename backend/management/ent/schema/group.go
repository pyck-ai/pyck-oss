package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

// Group holds the schema definition for the Group entity.
type Group struct {
	ent.Schema
}

// Fields of the Group.
func (Group) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.String("name").
			NotEmpty().
			Annotations(
				entgql.OrderField("NAME"),
			),
		field.String("description").
			Optional().
			Annotations(
				entgql.OrderField("DESCRIPTION"),
			),
	}
}

func (Group) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name", "tenant_id").
			Unique().
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()),
	}
}

// Edges of the Group.
func (Group) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("users", User.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("users"),
			),
		edge.To("roles", Role.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("roles"),
			),
	}
}

func (Group) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
		mixin.TenantMixin{},
	}
}

func (Group) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Schema("management"),
		entsql.Annotation{Table: "groups"},
		entgql.RelayConnection(),
		entgql.Type("Group"),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
		entgql.OrderField("name"),
		entgql.OrderField("description"),
		entgql.OrderField("tenant_id"),
	}
}
