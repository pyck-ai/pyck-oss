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

type User struct {
	ent.Schema
}

func (User) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Schema("management"),
		entsql.Table("users"),
		entgql.RelayConnection(),
		entgql.Type("User"),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("tenant", Tenant.Type).
			Ref("tenantUsers").
			Field("tenant_id").
			Unique().
			Required().
			Immutable(),
		edge.To("deviceUsersUsers", DeviceUser.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("deviceUsersUsers"),
			),
	}
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.String("idp_id").
			Immutable().
			Annotations(
				entgql.OrderField("IDP_ID"),
			),
		field.String("username").
			NotEmpty().
			Annotations(
				entgql.OrderField("USERNAME"),
			),
		field.String("email").
			Annotations(
				entgql.OrderField("EMAIL"),
			), // Email can be empty for zitadel machine users
		field.String("first_name").
			Annotations(
				entgql.OrderField("FIRST_NAME"),
			),
		field.String("last_name").
			Annotations(
				entgql.OrderField("LAST_NAME"),
			),
		field.Bool("is_admin").
			Default(false).
			Annotations(
				entgql.OrderField("IS_ADMIN"),
			),
	}
}

func (User) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("username", "email", "tenant_id").
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()).
			Unique(),
		index.Fields("idp_id", "tenant_id").
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()).
			Unique(),
	}
}

func (User) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
		mixin.TenantMixin{},
	}
}
