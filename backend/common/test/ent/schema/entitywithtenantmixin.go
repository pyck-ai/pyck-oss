package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

type EntityWithTenantMixin struct {
	ent.Schema
}

func (EntityWithTenantMixin) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
	}
}

func (EntityWithTenantMixin) Fields() []ent.Field {
	return []ent.Field{
		field.String("string_field").Default(""),
	}
}
