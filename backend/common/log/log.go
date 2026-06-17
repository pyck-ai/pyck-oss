package log

import (
	"context"
	"os"
	"runtime"
	"time"

	"github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/requestid"
	"github.com/pyck-ai/pyck/backend/common/tenantid"
	"github.com/pyck-ai/pyck/backend/common/typing"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel/trace"
)

const errorStackMarshallerSkipFrames = 3

type (
	Logger   = zerolog.Logger
	Level    = zerolog.Level
	LevelStr = typing.LogLevel
	Options  = config.LogConfig
)

const (
	DebugLevel = zerolog.DebugLevel
	InfoLevel  = zerolog.InfoLevel
	WarnLevel  = zerolog.WarnLevel
	ErrorLevel = zerolog.ErrorLevel
	FatalLevel = zerolog.FatalLevel
	PanicLevel = zerolog.PanicLevel

	CallersFieldName = "callers"
)

func ParseLevel(level string) (zerolog.Level, error) {
	return zerolog.ParseLevel(level)
}

func Enabled(logger Logger, level zerolog.Level) bool {
	return logger.GetLevel() <= level
}

func ForContext(ctx context.Context) *Logger {
	// zerolog's log.Ctx returns the logger stored in ctx but does not attach
	// ctx to the returned logger. Without that attachment, hooks that read
	// e.GetCtx() (addTraces, addRequestID) see context.Background() and
	// silently drop their fields. We materialize a derived logger that
	// carries ctx so events inherit it via Logger.newEvent.
	l := log.Ctx(ctx).With().Ctx(ctx).Logger()
	return &l
}

func Context(ctx context.Context, logger Logger) context.Context {
	return logger.WithContext(ctx)
}

func DefaultLogger() Logger {
	return log.Logger
}

func SetupLogger(ctx context.Context, serviceName string, config config.LogConfig) (context.Context, Logger) {
	zerolog.SetGlobalLevel(config.LogLevel.AsZeroLogLevel())

	zerolog.TimeFieldFormat = time.RFC3339Nano
	zerolog.TimestampFunc = timestampFunc

	logger := zerolog.
		New(os.Stderr).
		With().
		Timestamp().
		Caller().
		Stack().
		Str("service", serviceName).
		Logger().
		Hook(zerolog.HookFunc(addCallers)).
		Hook(zerolog.HookFunc(addTraces)).
		Hook(zerolog.HookFunc(addRequestID)).
		Hook(zerolog.HookFunc(addTenantID))

	switch config.LogFormat {
	case "console":
		logger = logger.Output(zerolog.ConsoleWriter{
			Out: os.Stderr,
		})
	default:
	}

	log.Logger = logger

	zerolog.SetGlobalLevel(config.LogLevel.AsZeroLogLevel())

	ctx = Context(ctx, logger)

	return ctx, logger
}

func timestampFunc() time.Time {
	return time.Now().UTC()
}

func addCallers(e *zerolog.Event, level zerolog.Level, message string) {
	if level < zerolog.ErrorLevel {
		return
	}

	// Start with a reasonable buffer size and grow if needed
	pcs := make([]uintptr, 16)
	n := runtime.Callers(errorStackMarshallerSkipFrames, pcs) // Skip runtime.Callers and this function

	// If we filled the buffer, the stack might be deeper - grow and retry
	for n == len(pcs) && len(pcs) < 256 { // Cap at 256 to prevent runaway growth
		pcs = make([]uintptr, len(pcs)*2)
		n = runtime.Callers(2, pcs)
	}

	// Convert to frames
	frames := runtime.CallersFrames(pcs[:n])
	result := make([]map[string]any, 0, n)

	for {
		frame, more := frames.Next()

		result = append(result, map[string]any{
			"file": frame.File,
			"line": frame.Line,
			"func": frame.Function,
		})

		if !more {
			break
		}
	}

	e.Any(CallersFieldName, result)
}

type traceInfo struct {
	TraceID string `json:"trace-id"`
	SpanID  string `json:"span-id"`
}

func (t traceInfo) MarshalZerologObject(e *zerolog.Event) {
	e.Str("trace-id", t.TraceID)
	e.Str("span-id", t.SpanID)
}

func addTraces(e *zerolog.Event, level zerolog.Level, message string) {
	ctx := e.GetCtx()
	span := trace.SpanFromContext(ctx)
	spanCtx := span.SpanContext()

	if !spanCtx.IsValid() {
		return
	}

	traceID := spanCtx.TraceID().String()
	spanID := spanCtx.SpanID().String()

	// Flat, snake_case fields (Elastic Common Schema / OTel logs data model).
	// The log collector reads these to populate the indexed
	// otel_logs.TraceId / SpanId columns, enabling log<->trace correlation.
	e.Str("trace_id", traceID)
	e.Str("span_id", spanID)

	// Nested object retained for backwards compatibility with existing
	// queries and dashboards.
	e.Any("trace", &traceInfo{
		TraceID: traceID,
		SpanID:  spanID,
	})
}

func addRequestID(e *zerolog.Event, level zerolog.Level, message string) {
	ctx := e.GetCtx()
	if ctx == nil {
		return
	}

	id := requestid.FromContext(ctx)
	if id == "" {
		return
	}

	e.Str(requestid.LogField, id)
}

func addTenantID(e *zerolog.Event, level zerolog.Level, message string) {
	ctx := e.GetCtx()
	if ctx == nil {
		return
	}

	tenantIDs := tenantid.FromContext(ctx)
	if len(tenantIDs) == 0 {
		return
	}

	e.Str(tenantid.LogField, tenantid.String(tenantIDs))
}
