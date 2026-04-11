package mocks

import (
	"context"
	"sync"

	"github.com/nats-io/nats.go/jetstream"
)

type jetstreamConsumeContext struct {
	mu        sync.Mutex
	closedCh  chan struct{}
	stoppedCh chan struct{}
	drained   bool
}

func newJetstreamConsumeContext() *jetstreamConsumeContext {
	return &jetstreamConsumeContext{
		closedCh:  make(chan struct{}),
		stoppedCh: make(chan struct{}),
	}
}

func (c *jetstreamConsumeContext) Stop() {
	c.mu.Lock()
	select {
	case <-c.stoppedCh:
	default:
		close(c.stoppedCh)
	}
	c.mu.Unlock()
}

func (c *jetstreamConsumeContext) Drain() {
	c.mu.Lock()
	if !c.drained {
		c.drained = true
		select {
		case <-c.closedCh:
		default:
			close(c.closedCh)
		}
	}
	c.mu.Unlock()
}

func (c *jetstreamConsumeContext) Closed() <-chan struct{} { return c.closedCh }

type JetstreamConsumer struct {
	mu      sync.Mutex
	handler jetstream.MessageHandler
	readyCh chan struct{}
}

func (c *JetstreamConsumer) Consume(h jetstream.MessageHandler, _ ...jetstream.PullConsumeOpt) (jetstream.ConsumeContext, error) {
	c.mu.Lock()
	c.handler = h
	if c.readyCh == nil {
		c.readyCh = make(chan struct{})
	}
	select {
	case <-c.readyCh:
	default:
		close(c.readyCh)
	}
	c.mu.Unlock()
	return newJetstreamConsumeContext(), nil
}

// Ready returns a channel closed when the handler is registered.
func (c *JetstreamConsumer) Ready() <-chan struct{} {
	c.mu.Lock()
	ch := c.readyCh
	if ch == nil {
		ch = make(chan struct{})
		c.readyCh = ch
	}
	c.mu.Unlock()
	return ch
}

// Emit delivers a message to the registered handler asynchronously.
func (c *JetstreamConsumer) Emit(msg jetstream.Msg) {
	c.mu.Lock()
	h := c.handler
	c.mu.Unlock()
	if h != nil {
		go h(msg)
	}
}

// Interface stubs (unused in these tests).
func (c *JetstreamConsumer) Info(context.Context) (*jetstream.ConsumerInfo, error) { return nil, nil }
func (c *JetstreamConsumer) Messages(...jetstream.PullMessagesOpt) (jetstream.MessagesContext, error) {
	return nil, nil
}
func (c *JetstreamConsumer) Fetch(int, ...jetstream.FetchOpt) (jetstream.MessageBatch, error) {
	return nil, nil
}
func (c *JetstreamConsumer) FetchBytes(int, ...jetstream.FetchOpt) (jetstream.MessageBatch, error) {
	return nil, nil
}
func (c *JetstreamConsumer) FetchNoWait(int) (jetstream.MessageBatch, error) { return nil, nil }
func (c *JetstreamConsumer) CachedInfo() *jetstream.ConsumerInfo             { return nil }
func (c *JetstreamConsumer) Next(...jetstream.FetchOpt) (jetstream.Msg, error) {
	return nil, nil
}
