package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

type EntityWithLimitMixin struct {
	ent.Schema
}

func (EntityWithLimitMixin) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.LimitMixin{},
	}
}

func (EntityWithLimitMixin) Fields() []ent.Field {
	return []ent.Field{
		field.String("string_field").Default(""),
	}
}
