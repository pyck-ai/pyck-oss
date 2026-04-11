package mocks

import (
	"context"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// JetstreamMsg minimally implements jetstream.Msg used by the listener callback.
type JetstreamMsg struct {
	mu       sync.Mutex
	DataByte []byte
	acked    bool
	naked    bool
	termed   bool

	once sync.Once
	done chan struct{}
}

func (m *JetstreamMsg) init() {
	m.mu.Lock()
	if m.done == nil {
		m.done = make(chan struct{})
	}
	m.mu.Unlock()
}
func (m *JetstreamMsg) signal() { m.once.Do(func() { close(m.done) }) }

func (m *JetstreamMsg) Wait(ctx context.Context) error {
	m.init()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *JetstreamMsg) Data() []byte                              { return m.DataByte }
func (m *JetstreamMsg) Ack() error                                { m.setAcked(true); m.signal(); return nil }
func (m *JetstreamMsg) DoubleAck(context.Context) error           { return m.Ack() }
func (m *JetstreamMsg) Nak() error                                { m.setNaked(true); m.signal(); return nil }
func (m *JetstreamMsg) NakWithDelay(time.Duration) error          { return m.Nak() }
func (m *JetstreamMsg) Term() error                               { m.setTermed(true); m.signal(); return nil }
func (m *JetstreamMsg) TermWithReason(string) error               { return m.Term() }
func (m *JetstreamMsg) Metadata() (*jetstream.MsgMetadata, error) { return nil, nil }
func (m *JetstreamMsg) Subject() string                           { return "" }
func (m *JetstreamMsg) Reply() string                             { return "" }
func (m *JetstreamMsg) Headers() nats.Header                      { return nil }
func (m *JetstreamMsg) InProgress() error                         { return nil }
func (m *JetstreamMsg) setAcked(v bool)                           { m.mu.Lock(); m.acked = v; m.mu.Unlock() }
func (m *JetstreamMsg) setNaked(v bool)                           { m.mu.Lock(); m.naked = v; m.mu.Unlock() }
func (m *JetstreamMsg) setTermed(v bool)                          { m.mu.Lock(); m.termed = v; m.mu.Unlock() }
func (m *JetstreamMsg) IsAcked() bool                             { m.mu.Lock(); defer m.mu.Unlock(); return m.acked }
func (m *JetstreamMsg) IsNaked() bool                             { m.mu.Lock(); defer m.mu.Unlock(); return m.naked }
func (m *JetstreamMsg) IsTermed() bool                            { m.mu.Lock(); defer m.mu.Unlock(); return m.termed }
