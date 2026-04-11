package mocks

import (
	"context"
	"github.com/nats-io/nats.go/jetstream"
)

// MockMessageClient is a mock implementation of MessageClient for testing.
type MockMessageClient struct {
	AckedMessages map[string]bool
}

// SendMessage does nothing in the mock implementation.
func (m *MockMessageClient) SendMessage(subject string, data []byte) error {
	return nil
}

// Ack acknowledges the message in the mock implementation.
func (m *MockMessageClient) Ack(subject string) error {
	m.AckedMessages[subject] = true
	return nil
}

// MockMessageConsumer is a mock implementation of MessageConsumer for testing.
type MockMessageConsumer struct{}

func (m *MockMessageConsumer) Consume(handler jetstream.MessageHandler, opts ...jetstream.PullConsumeOpt) (jetstream.ConsumeContext, error) {
	return nil, nil
}

func (m *MockMessageConsumer) Fetch(batch int, opts ...jetstream.FetchOpt) (jetstream.MessageBatch, error) {
	return nil, nil
}

func (m *MockMessageConsumer) FetchBytes(maxBytes int, opts ...jetstream.FetchOpt) (jetstream.MessageBatch, error) {
	return nil, nil
}

func (m *MockMessageConsumer) FetchNoWait(batch int) (jetstream.MessageBatch, error) {
	return nil, nil
}

func (m *MockMessageConsumer) Messages(opts ...jetstream.PullMessagesOpt) (jetstream.MessagesContext, error) {
	return nil, nil
}

func (m *MockMessageConsumer) Next(opts ...jetstream.FetchOpt) (jetstream.Msg, error) {
	return nil, nil
}

func (m *MockMessageConsumer) Info(ctx context.Context) (*jetstream.ConsumerInfo, error) {
	return nil, nil
}

func (m *MockMessageConsumer) CachedInfo() *jetstream.ConsumerInfo {
	return nil
}
