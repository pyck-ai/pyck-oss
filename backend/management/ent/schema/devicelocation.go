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
	"github.com/pyck-ai/pyck/backend/common/importexport"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

type DeviceLocation struct {
	ent.Schema
}

func (DeviceLocation) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entgql.Type("DeviceLocation"),
		entsql.Schema("management"),
		entsql.Table("device_locations"),
		entgql.RelayConnection(),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
		entgql.Directives(importexport.Importable("",
			importexport.WithList("deviceLocations"),
			importexport.WithCreate("setDeviceLocation"),
		)),
	}
}

func (DeviceLocation) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.UUID("device_id", uuid.UUID{}),
		field.UUID("location_id", uuid.UUID{}),
	}
}

func (DeviceLocation) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("device", Device.Type).
			Ref("deviceLocationsDevice").
			Field("device_id").
			Unique().
			Required(),
		edge.From("location", Location.Type).
			Ref("deviceLocationsLocation").
			Field("location_id").
			Unique().
			Required(),
	}
}

func (DeviceLocation) Indexes() []ent.Index {
	return []ent.Index{}
}

func (DeviceLocation) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
