package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

type EntityWithUserMixin struct {
	ent.Schema
}

func (EntityWithUserMixin) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.UserMixin{},
	}
}

func (EntityWithUserMixin) Fields() []ent.Field {
	return []ent.Field{
		field.String("string_field").Default(""),
	}
}
