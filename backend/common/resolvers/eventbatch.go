package resolvers

import (
	"context"
	"sync"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/utils"
)

type eventItem struct {
	ev         *events.MutationEventMessage
	withReply  bool
	diffBefore interface{}
	diffAfter  interface{}
	diffField  string
}

type EventBatch struct {
	publisher events.Publisher

	mu    sync.Mutex
	items []eventItem
}

func NewEventBatch(publisher events.Publisher) *EventBatch {
	return &EventBatch{publisher: publisher}
}

func (b *EventBatch) Add(ev *events.MutationEventMessage, withReply bool) {
	b.mu.Lock()
	newItems := make([]eventItem, len(b.items)+1)
	copy(newItems, b.items)
	newItems[len(b.items)] = eventItem{ev: ev, withReply: withReply}
	b.items = newItems
	b.mu.Unlock()
}

func (b *EventBatch) AddWithDiff(ev *events.MutationEventMessage, before, after interface{}, field string) {
	b.mu.Lock()
	newItems := make([]eventItem, len(b.items)+1)
	copy(newItems, b.items)
	newItems[len(b.items)] = eventItem{
		ev:         ev,
		diffBefore: before,
		diffAfter:  after,
		diffField:  field,
	}
	b.items = newItems
	b.mu.Unlock()
}

// Flush sends all queued events now (call right before returning from resolver).
func (b *EventBatch) Flush(ctx context.Context) error {
	// Take a snapshot and clear the queue under the lock.
	b.mu.Lock()
	toSend := b.items
	b.items = nil
	b.mu.Unlock()

	// Publish outside the lock.
	for _, it := range toSend {
		var err error
		if it.withReply {
			_, err = b.publisher.SendMutationEventWithReply(ctx, it.ev)
		} else {
			err = b.publisher.SendMutationEvent(ctx, it.ev)
		}

		if err != nil {
			return err
		}

		if it.diffBefore != nil {
			utils.SendUpdatedFieldsEventsAsync(ctx, b.publisher, *it.ev, it.diffBefore, it.diffAfter, it.diffField)
		}
	}
	return nil
}
