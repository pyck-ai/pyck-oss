package log_test

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pyckLog "github.com/pyck-ai/pyck/backend/common/log"
)

// A fault site is reached from every request goroutine at once: that is the
// whole reason the throttle exists. These tests cover what the sequential
// ones cannot: that concurrency neither multiplies the lines nor loses the
// count, and that a dropped occurrence is genuinely silent rather than a
// line at some other level.

// syncBuffer serializes the writes of the concurrent loggers below;
// zerolog does not synchronize its writer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) buffer() *bytes.Buffer {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.NewBufferString(b.buf.String())
}

// A dependency that fails for every one of a hundred concurrent requests
// must still produce one line. A throttle that let the CAS losers log (or
// demoted them to Debug, as it used to) would write a hundred, which is
// the log-pipeline saturation the throttle exists to prevent.
func TestFaultThrottle_ConcurrentOccurrencesEmitOneLine(t *testing.T) {
	t.Parallel()

	const goroutines = 100
	out := &syncBuffer{}
	logger := zerolog.New(out).Level(zerolog.DebugLevel)
	throttle := pyckLog.NewFaultThrottle(time.Hour)

	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logFault(&logger, throttle)
		}()
	}
	wg.Wait()

	lines := readJSONLines(t, out.buffer())
	require.Len(t, lines, 1, "one window, one line, whatever the concurrency")
	assert.Equal(t, "error", lines[0]["level"], "a CAS loser must be dropped, never demoted")
}

// None of the concurrent occurrences may be lost from the count either: the
// next window's line is what tells an operator the real volume of the
// outage, and a count that under-reported it would make a fleet-wide
// failure look like a single blip.
func TestFaultThrottle_ConcurrentOccurrencesAreAllCounted(t *testing.T) {
	t.Parallel()

	const goroutines = 50
	const window = 20 * time.Millisecond

	out := &syncBuffer{}
	logger := zerolog.New(out).Level(zerolog.DebugLevel)
	throttle := pyckLog.NewFaultThrottle(window)

	logFault(&logger, throttle) // opens the window
	opened := time.Now()

	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logFault(&logger, throttle)
		}()
	}
	wg.Wait()

	// Wait the window out, then let the next occurrence close it.
	for time.Since(opened) < window {
		time.Sleep(time.Millisecond)
	}
	logFault(&logger, throttle)

	lines := readJSONLines(t, out.buffer())
	require.Len(t, lines, 2, "the second window opens exactly one further line")
	suppressed, ok := lines[1]["suppressed"].(float64)
	require.True(t, ok)
	assert.InDelta(t, float64(goroutines), suppressed, 0.0,
		"every concurrent occurrence counts towards the line that closes the window")
}

// A dropped occurrence produces nothing at all, even when the call site
// decorates it: fault sites add fields, and a disabled event that still
// wrote them would put the volume back in the pipeline the throttle just
// removed it from.
func TestFaultThrottle_DroppedOccurrenceWritesNothingEvenWithFields(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := zerolog.New(&buf).Level(zerolog.TraceLevel)
	throttle := pyckLog.NewFaultThrottle(time.Hour)

	throttle.Err(&logger, assert.AnError).Str("source", "management").Msg("first")
	before := buf.Len()

	for range 5 {
		throttle.Err(&logger, assert.AnError).
			Str("source", "management").
			Int("tenants", 12).
			Dur("waited", time.Second).
			Msg("dropped")
	}

	assert.Equal(t, before, buf.Len(), "a dropped occurrence must write no bytes, decorated or not")
	lines := readJSONLines(t, &buf)
	require.Len(t, lines, 1)
	assert.Equal(t, "first", lines[0]["message"])
}

// Throttles are constructed one per fault site so unrelated faults do not
// silence each other: a shared one would hide a second outage behind the
// first for a whole window.
func TestFaultThrottle_SitesDoNotThrottleEachOther(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := zerolog.New(&buf).Level(zerolog.DebugLevel)
	first := pyckLog.NewFaultThrottle(time.Hour)
	second := pyckLog.NewFaultThrottle(time.Hour)

	logFault(&logger, first)
	logFault(&logger, second)
	logFault(&logger, first)  // dropped
	logFault(&logger, second) // dropped

	lines := readJSONLines(t, &buf)
	require.Len(t, lines, 2, "each site reports its own first occurrence")
	for _, line := range lines {
		assert.Equal(t, "error", line["level"])
	}
}
