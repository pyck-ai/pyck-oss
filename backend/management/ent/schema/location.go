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

type Location struct {
	ent.Schema
}

func (Location) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entgql.Type("Location"),
		entsql.Schema("management"),
		entsql.Table("locations"),
		entgql.RelayConnection(),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

func (Location) Fields() []ent.Field {
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

func (Location) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("deviceLocationsLocation", DeviceLocation.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("deviceLocationsLocation"),
			),
	}
}

func (Location) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "name").
			Unique().
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()),
	}
}

func (Location) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
