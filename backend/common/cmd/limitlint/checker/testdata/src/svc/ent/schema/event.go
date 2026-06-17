package schema

import "mixin"

// Event has no LimitMixin — Query().All(ctx) on this entity must NOT
// be flagged.
type Event struct {
	mixin.Schema
}

func (Event) Mixin() []any {
	return []any{
		mixin.HistoryMixin{},
	}
}
