package tenants

import "time"

// TriggerConfig bounds the tenant-lifecycle trigger consumer. Built in main
// from the env config, which supplies a default for every field, so both
// bounds are always set.
type TriggerConfig struct {
	// MaxRedeliver caps the deliveries (the first one included) before
	// JetStream drops a tenant event.
	MaxRedeliver int
	// TemporalCallTimeout bounds the one Temporal start the handler makes
	// per delivery, so a hung Temporal frontend cannot pin the consumer's
	// callback (and with it every later tenant event) indefinitely. The
	// consumer's AckWait is derived from it (ackWait), so JetStream does
	// not redeliver an event whose handler is still inside that call.
	TemporalCallTimeout time.Duration
}

const (
	// ackWaitMargin is what the consumer's AckWait adds on top of
	// TemporalCallTimeout: the handler's work around the Temporal call
	// (decoding the event, the ACK or NAK round-trip) has to fit in it.
	ackWaitMargin = 5 * time.Second

	// triggerPullBatch is how many tenant events the consumer holds at
	// once. It must stay 1: the consumer's AckWait covers one handler's
	// Temporal call (ackWait), not the time a prefetched message waits for
	// the handlers queued ahead of it.
	triggerPullBatch = 1
)

// ackWait is the consumer's AckWait: longer than the one Temporal call a
// delivery makes. Together with the one-message pull (triggerPullBatch),
// which starts a message's AckWait when its handler starts, an event is
// only redelivered on ACK timeout when its handler is gone, not while it
// is still waiting on Temporal.
func (c TriggerConfig) ackWait() time.Duration {
	return c.TemporalCallTimeout + ackWaitMargin
}

// redeliverBackoff is the delay before a NAK'd tenant event is delivered
// again, indexed by the delivery that failed (the last entry repeats). The
// handler applies it with NakWithDelay: a plain NAK is redelivered at once,
// and the consumer's BackOff only paces ACK-timeout redeliveries while it
// also shortens AckWait to its first entry. At the default MaxRedeliver of
// 5, a create event is dropped by a Temporal outage that outlasts the four
// delays (2+5+15+30 = 52 s) plus the five failed start calls: about 52 s
// when Temporal refuses at once, up to about 102 s when every call runs
// into the default TemporalCallTimeout of 10 s.
var redeliverBackoff = []time.Duration{
	2 * time.Second,
	5 * time.Second,
	15 * time.Second,
	30 * time.Second,
	60 * time.Second,
}
