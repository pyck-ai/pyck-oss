package log

import (
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
)

// FaultLogWindow is the default window of a [FaultThrottle]: long enough
// that one Error line per window per replica stays readable during a
// fleet-wide outage, short enough that an ongoing fault keeps being
// reported.
const FaultLogWindow = time.Minute

// FaultThrottle samples a repeating fault: the first occurrence in each
// window is logged at the level the site asks for ([FaultThrottle.Err] at
// Error, [FaultThrottle.Warn] at Warn) carrying the number of occurrences
// dropped since the previous line, and the rest produce no line at all.
//
// It exists for faults reported once per request: a shared dependency that
// goes down produces one line per request per replica for as long as the
// outage lasts, which saturates the log pipeline and buries the line naming
// the cause exactly when an operator is looking for it.
//
// The level never changes. Demoting the repeats to Debug would still emit
// them at the request rate and, worse, would make the fault invisible in
// the deployments that ship at Info; the throttle's job is to drop lines,
// not to reclassify a fault as routine.
//
// Safe for concurrent use; construct one per fault site (in the owning
// struct) so unrelated faults do not throttle each other.
type FaultThrottle struct {
	window time.Duration
	// lastNanos is the wall-clock time of the last emitted occurrence, as
	// UnixNano; suppressed counts the occurrences dropped since then. Both
	// are read and written from every request goroutine.
	lastNanos  atomic.Int64
	suppressed atomic.Int64
}

// NewFaultThrottle returns a throttle that emits one line per window
// (FaultLogWindow when window is not positive).
func NewFaultThrottle(window time.Duration) *FaultThrottle {
	if window <= 0 {
		window = FaultLogWindow
	}
	return &FaultThrottle{window: window}
}

// first reports whether this occurrence of the fault opens a new window
// (and is therefore the one to emit) and how many occurrences were dropped
// since the previously emitted one. A caller that loses the CAS race is not
// first: the winner is writing this window's line already. Every call counts
// as one occurrence, so call it exactly once per occurrence.
func (t *FaultThrottle) first() (first bool, suppressed int64) {
	now := time.Now().UnixNano()
	if last := t.lastNanos.Load(); now-last >= int64(t.window) && t.lastNanos.CompareAndSwap(last, now) {
		return true, t.suppressed.Swap(0)
	}
	t.suppressed.Add(1)
	return false, 0
}

// Err opens the log event for one occurrence of the repeating fault on
// logger, at Error level and carrying err plus the dropped-occurrence
// count: the line every fault site would otherwise assemble by hand. An
// occurrence inside the current window gets a disabled event instead, so
// finishing it produces nothing. Finish it like [Logger.Err], adding
// fields as needed:
//
//	t.Err(log.ForContext(ctx), err).Msg("lookup failed; using cached verdicts")
func (t *FaultThrottle) Err(logger *Logger, err error) *zerolog.Event {
	return t.event(logger, ErrorLevel).Err(err)
}

// Warn is Err's level for a degraded state that is expected under load
// rather than a failure of a dependency: a bound being hit, not something
// breaking. Finish it with Msg like Err; it carries no error of its own,
// so add one with .Err(err) when there is one.
//
//	t.Warn(log.ForContext(ctx)).Str("field", field).Msg("operation refused")
func (t *FaultThrottle) Warn(logger *Logger) *zerolog.Event {
	return t.event(logger, WarnLevel)
}

// event opens this occurrence's event at level, or a disabled one when the
// occurrence falls inside the current window. The level is the caller's:
// demoting a repeat would still emit it at the request rate and would
// reclassify the fault, which is not what a throttle is for.
func (t *FaultThrottle) event(logger *Logger, level Level) *zerolog.Event {
	first, suppressed := t.first()
	if !first {
		// zerolog.Disabled yields a nil event; every Event method and Msg
		// is nil-safe, so the caller's chain is a no-op.
		//nolint:zerologlint // the event is returned for the caller to finish.
		return logger.WithLevel(zerolog.Disabled)
	}

	//nolint:zerologlint // the event is returned for the caller to finish
	// with Msg, exactly like Logger.Err; every call site dispatches it.
	return logger.WithLevel(level).Int64("suppressed", suppressed)
}
