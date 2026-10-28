package log_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pyckLog "github.com/pyck-ai/pyck/backend/common/log"
)

// logFault writes one occurrence of a throttled fault through
// FaultThrottle.Err (the exact call the liveness-fault paths in authn,
// tenant, nats, file and temporal make) so these tests double as the
// shared helper's direct coverage (emitted or dropped by the throttle,
// suppressed count attached).
func logFault(logger *zerolog.Logger, throttle *pyckLog.FaultThrottle) {
	throttle.Err(logger, assert.AnError).Msg("source failed")
}

// readJSONLines parses every line written to buf.
func readJSONLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &got))
		out = append(out, got)
	}
	return out
}

// A dependency that fails on every request must not write an Error line per
// request: the first fault of a window is reported at Error and the rest
// produce no line at all. The level never changes: a repeat demoted to
// Debug would still be written at the request rate, and would be invisible
// in a deployment that ships at Info.
func TestFaultThrottle_OneErrorPerWindow(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := zerolog.New(&buf).Level(zerolog.DebugLevel)
	throttle := pyckLog.NewFaultThrottle(time.Hour)

	for range 5 {
		logFault(&logger, throttle)
	}

	lines := readJSONLines(t, &buf)
	require.Len(t, lines, 1, "only the first occurrence of the window is written")
	assert.Equal(t, "error", lines[0]["level"], "the site's level is never demoted")
	suppressed, ok := lines[0]["suppressed"].(float64)
	require.True(t, ok, "the Error line must carry the suppressed count")
	assert.Zero(t, suppressed, "nothing was suppressed before the first line")
}

// Once the window has elapsed the fault is reported again, and the Error
// line carries how many occurrences were dropped in the meantime: the
// volume stays visible without being emitted.
//
// The body runs in a synctest bubble: the three dropped occurrences must
// land inside the 1ms window the first one opens, and on the real clock a
// stalled runner can exceed that between two statements, report one of
// them at Error and reset the count. The bubble's fake clock only advances
// while every goroutine in it is blocked.
func TestFaultThrottle_NextErrorCarriesSuppressedCount(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var buf bytes.Buffer
		logger := zerolog.New(&buf).Level(zerolog.DebugLevel)
		throttle := pyckLog.NewFaultThrottle(time.Millisecond)

		logFault(&logger, throttle)
		for range 3 {
			logFault(&logger, throttle)
		}
		assert.Eventually(t, func() bool {
			buf.Reset()
			logFault(&logger, throttle)
			lines := readJSONLines(t, &buf)
			return len(lines) == 1 && lines[0]["level"] == "error"
		}, time.Second, time.Millisecond, "the fault must be reported again once the window elapsed")

		lines := readJSONLines(t, &buf)
		require.Len(t, lines, 1)
		suppressed, ok := lines[0]["suppressed"].(float64)
		require.True(t, ok, "the Error line must carry the suppressed count")
		assert.GreaterOrEqual(t, suppressed, 3.0, "every dropped occurrence must be counted")
	})
}

// The suppressed count belongs to the window the Error line closes, not to
// the process: it is reset by every emitted line, so an operator reading
// two successive lines during a long outage sees the volume of each window
// rather than a running total that only ever grows.
//
// The body runs in a synctest bubble for the same reason as
// TestFaultThrottle_NextErrorCarriesSuppressedCount: the exact count of 3
// needs every dropped occurrence inside the window, which a runner stall
// longer than the window breaks on the real clock.
func TestFaultThrottle_SuppressedCountResetsAfterEachError(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const window = 50 * time.Millisecond

		var buf bytes.Buffer
		logger := zerolog.New(&buf).Level(zerolog.DebugLevel)
		throttle := pyckLog.NewFaultThrottle(window)

		logFault(&logger, throttle) // opens the first window at Error
		windowOpened := time.Now()
		for range 3 {
			logFault(&logger, throttle) // dropped
		}

		first := errorLineAfterWindow(t, &buf, &logger, throttle, windowOpened, window)
		require.InDelta(t, 3.0, first, 0.0,
			"the Error line reports the occurrences dropped in the window it closes")

		// A second window in which nothing was dropped: the count is the volume
		// of that window, so it starts again from zero.
		windowOpened = time.Now()
		second := errorLineAfterWindow(t, &buf, &logger, throttle, windowOpened, window)
		assert.Zero(t, second, "a window with no dropped occurrences reports 0, not the running total")
	})
}

// errorLineAfterWindow waits until window has elapsed since opened — the
// throttle allows the next Error line only then — logs one occurrence and
// returns the suppressed count it carried. It fails the test if that
// occurrence was not reported at Error.
func errorLineAfterWindow(
	t *testing.T,
	buf *bytes.Buffer,
	logger *zerolog.Logger,
	throttle *pyckLog.FaultThrottle,
	opened time.Time,
	window time.Duration,
) float64 {
	t.Helper()

	require.Eventually(t, func() bool { return time.Since(opened) > window },
		time.Second, time.Millisecond, "the throttle window must elapse")

	buf.Reset()
	logFault(logger, throttle)
	lines := readJSONLines(t, buf)
	require.Len(t, lines, 1)
	require.Equal(t, "error", lines[0]["level"], "the window elapsed, so the fault is reported again")
	suppressed, ok := lines[0]["suppressed"].(float64)
	require.True(t, ok, "the Error line must carry the suppressed count")
	return suppressed
}

// A non-positive window is a wiring mistake, not a request to log every
// fault: the default window applies.
func TestFaultThrottle_ZeroWindowUsesDefault(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := zerolog.New(&buf).Level(zerolog.DebugLevel)
	throttle := pyckLog.NewFaultThrottle(0)

	logFault(&logger, throttle)
	logFault(&logger, throttle)

	lines := readJSONLines(t, &buf)
	require.Len(t, lines, 1, "the default window swallows the second occurrence")
	assert.Equal(t, "error", lines[0]["level"])
}
