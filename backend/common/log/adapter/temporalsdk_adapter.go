package adapter

import (
	"fmt"
	"math"

	"github.com/pyck-ai/pyck/backend/common/log"
	temporalsdklog "go.temporal.io/sdk/log"
)

const (
	temporalSDKAdapterCallerSkip = 3
)

// TemporalSDKLogAdapter adapts a zerolog logger to a Temporal SDK logger.
func TemporalSDKLogAdapter(logger log.Logger) temporalsdklog.Logger {
	logger = logger.With().
		CallerWithSkipFrameCount(temporalSDKAdapterCallerSkip).
		Logger()

	return &temporalSDKLogAdapter{
		logger: logger,
	}
}

type temporalSDKLogAdapter struct {
	logger log.Logger
}

var (
	_ temporalsdklog.Logger          = (*temporalSDKLogAdapter)(nil)
	_ temporalsdklog.WithLogger      = (*temporalSDKLogAdapter)(nil)
	_ temporalsdklog.WithSkipCallers = (*temporalSDKLogAdapter)(nil)
)

func (l *temporalSDKLogAdapter) fields(keyvals ...any) ([]any, error) {
	var (
		fields = make([]any, 0, int(math.Floor(float64(len(keyvals))/2)))
		err    error
	)

	for i := 0; i < len(keyvals); i += 2 {
		if i+1 >= len(keyvals) {
			break
		}

		var key string

		switch v := keyvals[i].(type) {
		case string:
			key = v
		default:
			key = fmt.Sprintf("%v", keyvals[i])
		}

		if key == "error" || key == "err" {
			if e, ok := keyvals[i+1].(error); ok {
				err = e
				continue
			}
		}

		fields = append(fields, key, keyvals[i+1])
	}

	return fields, err
}

// Debug implements log.Logger.
func (l *temporalSDKLogAdapter) Debug(msg string, keyvals ...any) {
	fields, err := l.fields(keyvals...)
	l.logger.Debug().Fields(fields).Err(err).Msg(msg)
}

// Error implements log.Logger.
func (l *temporalSDKLogAdapter) Error(msg string, keyvals ...any) {
	fields, err := l.fields(keyvals...)
	l.logger.Error().Fields(fields).Err(err).Msg(msg)
}

// Info implements log.Logger.
func (l *temporalSDKLogAdapter) Info(msg string, keyvals ...any) {
	fields, err := l.fields(keyvals...)
	l.logger.Info().Fields(fields).Err(err).Msg(msg)
}

// Warn implements log.Logger.
func (l *temporalSDKLogAdapter) Warn(msg string, keyvals ...any) {
	fields, err := l.fields(keyvals...)
	l.logger.Warn().Fields(fields).Err(err).Msg(msg)
}

// With implements log.WithLogger.
func (l *temporalSDKLogAdapter) With(keyvals ...any) temporalsdklog.Logger {
	fields, err := l.fields(keyvals...)
	logger := l.logger.With().Fields(fields).Err(err).Logger()

	return &temporalSDKLogAdapter{
		logger: logger,
	}
}

// Skip implements log.SkipLogger.
func (l *temporalSDKLogAdapter) WithCallerSkip(extraSkip int) temporalsdklog.Logger {
	logger := l.logger.With().CallerWithSkipFrameCount(extraSkip).Logger()

	return &temporalSDKLogAdapter{
		logger: logger,
	}
}
