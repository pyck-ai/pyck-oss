package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

type KeyValue struct {
	ent.Schema
}

func (KeyValue) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Schema("management"),
		entsql.Table("keyvalues"),
		entgql.RelayConnection(),
		entgql.QueryField(),
		entgql.Mutations(),
	}
}

func (KeyValue) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.String("name").
			Default("").
			Annotations(
				entgql.OrderField("NAME"),
			),
	}
}

func (KeyValue) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields(mixin.TenantFieldTenantID, mixin.UserFieldUserID, "name").
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()).
			Unique(),
	}
}

func (KeyValue) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.UserMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
