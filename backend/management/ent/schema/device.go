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

type Device struct {
	ent.Schema
}

func (Device) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entgql.Type("Device"),
		entsql.Schema("management"),
		entsql.Table("devices"),
		entgql.RelayConnection(),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

func (Device) Fields() []ent.Field {
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
	}
}

func (Device) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("deviceLocationsDevice", DeviceLocation.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("deviceLocationsDevice"),
			),
		edge.To("deviceUsersDevice", DeviceUser.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("deviceUsersDevice"),
			),
	}
}

func (Device) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "name").
			Unique().
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()),
	}
}

func (Device) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
