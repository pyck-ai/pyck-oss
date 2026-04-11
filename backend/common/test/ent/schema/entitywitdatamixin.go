package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

type EntityWithDataMixin struct {
	ent.Schema
}

func (EntityWithDataMixin) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.DataMixin{},
	}
}

func (EntityWithDataMixin) Fields() []ent.Field {
	return []ent.Field{
		field.String("string_field").Default(""),
	}
}
