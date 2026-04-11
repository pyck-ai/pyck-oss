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

type DeviceUser struct {
	ent.Schema
}

func (DeviceUser) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entgql.Type("DeviceUser"),
		entsql.Schema("management"),
		entsql.Table("device_users"),
		entgql.RelayConnection(),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

func (DeviceUser) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.UUID("device_id", uuid.UUID{}),
		field.UUID("user_id", uuid.UUID{}),
	}
}

func (DeviceUser) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("device", Device.Type).
			Ref("deviceUsersDevice").
			Field("device_id").
			Unique().
			Required(),
		edge.From("user", User.Type).
			Ref("deviceUsersUsers").
			Field("user_id").
			Unique().
			Required(),
	}
}

func (DeviceUser) Indexes() []ent.Index {
	return []ent.Index{}
}

func (DeviceUser) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
