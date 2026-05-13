package log_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pyckLog "github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/requestid"
)

// hookedLogger constructs a logger wired with the same request-id hook
// SetupLogger registers, but writes to an in-memory buffer for assertions.
func hookedLogger(buf *bytes.Buffer) zerolog.Logger {
	return zerolog.New(buf).Hook(zerolog.HookFunc(func(e *zerolog.Event, _ zerolog.Level, _ string) {
		ctx := e.GetCtx()
		if ctx == nil {
			return
		}
		id := requestid.FromContext(ctx)
		if id == "" {
			return
		}
		e.Str(requestid.LogField, id)
	}))
}

// readJSONLine parses a single JSON object from buf and returns it.
func readJSONLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	return got
}

func TestHookFiresWhenEventHasExplicitCtx(t *testing.T) {
	t.Parallel()

	const id = "01010101-0202-7303-8404-050505050505"

	ctx, err := requestid.WithRequestID(context.Background(), id)
	require.NoError(t, err)

	var buf bytes.Buffer
	logger := hookedLogger(&buf)

	logger.Info().Ctx(ctx).Msg("hello")

	assert.Equal(t, id, readJSONLine(t, &buf)[requestid.LogField])
}

func TestHookOmitsFieldWhenBaggageAbsent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := hookedLogger(&buf)

	logger.Info().Ctx(context.Background()).Msg("hello")

	_, present := readJSONLine(t, &buf)[requestid.LogField]
	assert.False(t, present, "log entry without baggage must not have request-id field")
}

// TestForContextProducesLoggerThatPropagatesCtxToHook is the regression
// test for a real bug discovered during review: zerolog.WithContext only
// stores the logger in ctx, it does NOT put ctx on the logger. So events
// created from log.Ctx(ctx) inherit context.Background() and hooks reading
// e.GetCtx() silently drop their fields. ForContext must materialize a
// derived logger that carries ctx so production-style call sites
// (`log.ForContext(ctx).Info().Msg(...)`, with no explicit `.Ctx(ctx)` on
// the event) still trigger the hook.
func TestForContextProducesLoggerThatPropagatesCtxToHook(t *testing.T) {
	t.Parallel()

	const id = "01010101-0202-7303-8404-050505050506"

	var buf bytes.Buffer
	base := hookedLogger(&buf)

	ctx := base.WithContext(context.Background())
	ctx, err := requestid.WithRequestID(ctx, id)
	require.NoError(t, err)

	// Production-style call: no explicit .Ctx(ctx) on the event.
	pyckLog.ForContext(ctx).Info().Msg("hello")

	assert.Equal(t, id, readJSONLine(t, &buf)[requestid.LogField])
}

// TestForContextWorksWithDerivedChildContext mirrors the realistic flow:
// the logger is attached at the root, then baggage is added to a deeper
// child context (e.g. by the HTTP middleware after the logger setup).
// The hook must still see the latest baggage from the child context, not
// the snapshot from when the logger was attached.
func TestForContextWorksWithDerivedChildContext(t *testing.T) {
	t.Parallel()

	const id = "01010101-0202-7303-8404-050505050507"

	var buf bytes.Buffer
	base := hookedLogger(&buf)

	rootCtx := base.WithContext(context.Background())
	childCtx, err := requestid.WithRequestID(rootCtx, id)
	require.NoError(t, err)

	pyckLog.ForContext(childCtx).Info().Msg("hello")

	assert.Equal(t, id, readJSONLine(t, &buf)[requestid.LogField])
}

func TestForContextWithEmptyBaggageDoesNotEmitField(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	base := hookedLogger(&buf)

	ctx := base.WithContext(context.Background())

	pyckLog.ForContext(ctx).Info().Msg("hello")

	_, present := readJSONLine(t, &buf)[requestid.LogField]
	assert.False(t, present)
}

// TestLogFieldNameMatchesConvention pins the snake_case field name aligned
// with Elastic Common Schema and the OTel logs data model. Changing this
// breaks downstream log search queries; require an explicit code change.
func TestLogFieldNameMatchesConvention(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "request_id", requestid.LogField)
	assert.NotNil(t, pyckLog.DefaultLogger())
}
