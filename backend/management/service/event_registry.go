package service

import (
	"context"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/memkv"
	"github.com/pyck-ai/pyck/backend/common/std"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/event"
	"github.com/rs/zerolog/log"
)

// EventRegistryService returns an instance of the EventRegistryService.
func NewEventRegistryService(client *ent.Client) (*EventRegistryService, error) {
	return &EventRegistryService{
		client:   client,
		memStore: memkv.NewInMemoryKVStore(0),
	}, nil
}

type EventRegistryService struct {
	client   *ent.Client
	memStore *memkv.InMemoryKVStore
}

func (e *EventRegistryService) ListenToEvents(ctx context.Context, consumer jetstream.Consumer) {
	ctx = authn.Context(ctx, authn.SystemUser())

	_, err := consumer.Consume(func(msg jetstream.Msg) {
		payload, err := std.UnmarshalJson[map[string]interface{}](msg.Data())
		if err != nil {
			log.Error().Stack().Err(err).Msg("Error when unmarshaling event payload")
			ackErr := msg.Ack() // we acknowledge also when error
			if ackErr != nil {
				log.Error().Stack().Err(err).Msg("Error when acknowledging event")
			}
			return
		}
		// TODO(michael): Instead of trying to parse the payload as a
		// map[string]any, all events should instead implement a GetType()
		// interface. This would allow to more easily check if the message type
		// is already known here and would not require all events to manually
		// set the Type field. They could instead simply embed a base struct...
		//
		// Different event message types use different JSON key casing:
		//   - MutationEventMessage uses json:"type" (lowercase)
		//   - CustomEventMessage has no json tag, so Go encodes it as "Type" (capital)
		// Try both to avoid "Missing event type" log spam for mutation events.
		eventType, ok := payload["type"]
		if !ok {
			eventType, ok = payload["Type"]
		}
		if !ok {
			log.Error().Stack().Msg("Event-Type is missing in message.")
			ackErr := msg.Ack()
			if ackErr != nil {
				log.Error().Stack().Err(err).Msg("Error when acknowledging event")
			}
			return
		}
		// save in database, if event type is unknown
		if _, exists := e.memStore.Get(eventType.(string)); !exists {
			log.Info().Str("event-type", eventType.(string)).Msg("Event is yet unknown")
			if err = e.SaveEvent(ctx, msg.Subject(), eventType.(string), payload); err != nil {
				log.Error().Stack().Err(err).Msg("Error when saving event to registry")
				ackErr := msg.Ack()
				if ackErr != nil {
					log.Error().Stack().Err(err).Msg("Error when acknowledging event")
				}
				return
			}
			e.memStore.Set(eventType.(string), true, 0)
		}
		err = msg.Ack()
		if err != nil {
			log.Error().Stack().Err(err).Msg("Error when acknowledging event")
		}
	})
	if err != nil {
		log.Error().Stack().Err(err).Msg("Error when processing event")
	}
}

func (e *EventRegistryService) SaveEvent(ctx context.Context, topic, name string, example map[string]interface{}) error {
	// Check first if an event with this topic already exists
	exists, err := e.client.Event.Query().
		Where(event.Topic(topic)).
		Exist(ctx)
	if err != nil {
		return err
	}

	// Create only if it doesn't exist yet
	if !exists {
		return e.client.Event.
			Create().
			SetTopic(topic).
			SetName(name).
			SetExample(example).
			Exec(ctx)
	}

	return nil // Already exists, ignore
}

func (e *EventRegistryService) PreloadEventsCache(ctx context.Context) error {
	ctx = authn.Context(ctx, authn.SystemUser())

	events, err := e.client.Event.Query().All(ctx)
	if err != nil {
		return err
	}

	for _, ev := range events {
		e.memStore.Set(ev.Name, true, 0)
	}

	return nil
}
