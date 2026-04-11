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

// AccessPolicy holds the schema definition for the AccessPolicy entity.
type AccessPolicy struct {
	ent.Schema
}

// Fields of the AccessPolicy.
func (AccessPolicy) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.String("resource").
			NotEmpty().
			Comment("Resource identifier (e.g., 'inventory.item')").
			Annotations(
				entgql.OrderField("RESOURCE"),
			),
		field.String("action").
			NotEmpty().
			Comment("Action to perform (e.g., 'read', 'write', 'delete')").
			Annotations(
				entgql.OrderField("ACTION"),
			),
		field.String("effect").
			Default("allow").
			Comment("Effect of the policy: 'allow' or 'deny'").
			Annotations(
				entgql.OrderField("EFFECT"),
			),
	}
}

func (AccessPolicy) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("resource", "action", "tenant_id", "deleted_at"),
		index.Fields("tenant_id", "deleted_at"),
	}
}

// Edges of the AuthPolicy.
func (AccessPolicy) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("role", Role.Type).
			Ref("policies").
			Unique().
			Required(),
	}
}

func (AccessPolicy) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
		mixin.TenantMixin{},
	}
}

func (AccessPolicy) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Schema("management"),
		entsql.Annotation{Table: "policies"},
		entgql.RelayConnection(),
		entgql.Type("AccessPolicy"),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
		entgql.OrderField("resource"),
		entgql.OrderField("action"),
		entgql.OrderField("effect"),
		entgql.OrderField("tenant_id"),
	}
}