package mocks

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// JetstreamMsg minimally implements jetstream.Msg used by the listener callback.
type JetstreamMsg struct {
	mu       sync.Mutex
	DataByte []byte
	// Deliveries is what Metadata reports as NumDelivered; zero stands for
	// a first delivery in tests that do not care.
	Deliveries uint64
	// MetadataErr, when set, is what Metadata fails with, so a test can
	// drive a handler's unreadable-delivery-count branch.
	MetadataErr error
	acked       bool
	naked       bool
	termed      bool
	// nakDelays holds the delay of every NAK in order; a plain Nak
	// records zero. Guarded by mu.
	nakDelays []time.Duration

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

func (m *JetstreamMsg) Data() []byte                    { return m.DataByte }
func (m *JetstreamMsg) Ack() error                      { m.setAcked(true); m.signal(); return nil }
func (m *JetstreamMsg) DoubleAck(context.Context) error { return m.Ack() }
func (m *JetstreamMsg) Nak() error                      { return m.NakWithDelay(0) }
func (m *JetstreamMsg) Term() error                     { m.setTermed(true); m.signal(); return nil }
func (m *JetstreamMsg) TermWithReason(string) error     { return m.Term() }
func (m *JetstreamMsg) Metadata() (*jetstream.MsgMetadata, error) {
	if m.MetadataErr != nil {
		return nil, m.MetadataErr
	}
	return &jetstream.MsgMetadata{NumDelivered: m.Deliveries}, nil
}
func (m *JetstreamMsg) Subject() string      { return "" }
func (m *JetstreamMsg) Reply() string        { return "" }
func (m *JetstreamMsg) Headers() nats.Header { return nil }
func (m *JetstreamMsg) InProgress() error    { return nil }
func (m *JetstreamMsg) setAcked(v bool)      { m.mu.Lock(); m.acked = v; m.mu.Unlock() }
func (m *JetstreamMsg) setTermed(v bool)     { m.mu.Lock(); m.termed = v; m.mu.Unlock() }
func (m *JetstreamMsg) IsAcked() bool        { m.mu.Lock(); defer m.mu.Unlock(); return m.acked }
func (m *JetstreamMsg) IsNaked() bool        { m.mu.Lock(); defer m.mu.Unlock(); return m.naked }
func (m *JetstreamMsg) IsTermed() bool       { m.mu.Lock(); defer m.mu.Unlock(); return m.termed }

// NakWithDelay records the NAK and its delay; Nak records a zero delay.
func (m *JetstreamMsg) NakWithDelay(delay time.Duration) error {
	m.mu.Lock()
	m.naked = true
	m.nakDelays = append(m.nakDelays, delay)
	m.mu.Unlock()
	m.signal()
	return nil
}

// NakDelays returns the delay of every NAK the message received, in order;
// a plain Nak shows up as zero, so a test can tell it from a delayed one.
func (m *JetstreamMsg) NakDelays() []time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.nakDelays)
}
