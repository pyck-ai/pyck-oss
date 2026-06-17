package schema

import "mixin"

type Item struct {
	mixin.Schema
}

func (Item) Mixin() []any {
	return []any{
		mixin.LimitMixin{},
		mixin.HistoryMixin{},
	}
}
