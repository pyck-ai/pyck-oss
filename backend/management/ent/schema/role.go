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

// Role holds the schema definition for the Role entity.
type Role struct {
	ent.Schema
}

// Fields of the Role.
func (Role) Fields() []ent.Field {
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

func (Role) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name", "tenant_id").
			Unique().
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()),
	}
}

// Edges of the Role.
func (Role) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("users", User.Type).
			Ref("roles").
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("users"),
			),
		edge.From("groups", Group.Type).
			Ref("roles").
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("groups"),
			),
		edge.To("policies", AccessPolicy.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("policies"),
			),
	}
}

func (Role) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
		mixin.TenantMixin{},
	}
}

func (Role) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Schema("management"),
		entsql.Annotation{Table: "roles"},
		entgql.RelayConnection(),
		entgql.Type("Role"),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
		entgql.OrderField("name"),
		entgql.OrderField("description"),
		entgql.OrderField("tenant_id"),
	}
}
