package events

import (
	"github.com/nats-io/nats.go"
)

func NewEventSubscriber(client *nats.Conn, streamName string) (*EventSubscriber, error) {
	return &EventSubscriber{
		client:     client,
		streamName: streamName,
	}, nil
}

type EventSubscriber struct {
	client     *nats.Conn
	streamName string
}

// Subscribe subscribes to the subject for events.
//
// The subscriber forwards raw nats.Msg values into eventsChannel. Consumers
// must call ContextFromMessage(ctx, msg) at the start of each handler to
// extract OTel trace context and baggage (including request-id) carried in
// the message headers.
func (s *EventSubscriber) Subscribe(eventsChannel chan *nats.Msg) (*nats.Subscription, error) {
	sub, err := s.client.Subscribe(s.streamName, func(msg *nats.Msg) {
		eventsChannel <- msg
	})
	if err != nil {
		return nil, err
	}
	return sub, nil
}

func (e *EventSubscriber) Close() {
	e.client.Close()
}
