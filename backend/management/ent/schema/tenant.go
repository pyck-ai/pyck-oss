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

type Tenant struct {
	ent.Schema
}

func (Tenant) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Schema("management"),
		entsql.Table("tenants"),
		entgql.RelayConnection(),
		entgql.Type("Tenant"),
		entgql.QueryField(),
	}
}

func (Tenant) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("tenantUsers", User.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("tenantUsers"),
			),
	}
}

func (Tenant) Fields() []ent.Field {
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
		field.String("idp_org_ref").
			Annotations(
				entgql.OrderField("IDP_ORG_REF"),
			),
	}
}

func (Tenant) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("idp_org_ref").
			Unique().
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()),
	}
}

func (Tenant) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
