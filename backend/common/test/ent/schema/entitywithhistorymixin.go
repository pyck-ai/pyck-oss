package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

type EntityWithHistoryMixin struct {
	ent.Schema
}

func (EntityWithHistoryMixin) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("string_field").Default(""),
	}
}

func (EntityWithHistoryMixin) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.HistoryMixin{},
	}
}

func (EntityWithHistoryMixin) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name").Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()),
	}
}
