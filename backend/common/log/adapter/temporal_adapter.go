package adapter

import (
	"math"

	"github.com/pyck-ai/pyck/backend/common/log"
	temporallog "go.temporal.io/server/common/log"
	"go.temporal.io/server/common/log/tag"
)

const (
	temporalAdapterCallerSkip = 3
	temporalErrorTag          = "Error"
)

// TemporalLogAdapter adapts a zerolog logger to a Temporal logger.
func TemporalLogAdapter(logger log.Logger) temporallog.Logger {
	loggerCtx := logger.With().
		CallerWithSkipFrameCount(temporalAdapterCallerSkip).
		Logger()

	return &temporalLogAdapter{
		logger: loggerCtx,
	}
}

type temporalLogAdapter struct {
	logger log.Logger
}

var (
	_ temporallog.Logger     = (*temporalLogAdapter)(nil)
	_ temporallog.WithLogger = (*temporalLogAdapter)(nil)
	_ temporallog.SkipLogger = (*temporalLogAdapter)(nil)
)

func (l *temporalLogAdapter) fields(tags []tag.Tag) ([]any, error) {
	var (
		fields = make([]any, 0, int(math.Floor(float64(len(tags))/2)))
		err    error
	)

	for i := 0; i < len(tags); i++ {
		switch tags[i].Key() {
		case temporalErrorTag:
			if e, ok := tags[i].Value().(error); ok {
				err = e
				continue
			}
		}

		fields = append(fields, tags[i].Key(), tags[i].Value())
	}

	return fields, err
}

// Debug implements log.Logger.
func (l *temporalLogAdapter) Debug(msg string, tags ...tag.Tag) {
	fields, err := l.fields(tags)
	l.logger.Debug().Fields(fields).Err(err).Msg(msg)
}

// Error implements log.Logger.
func (l *temporalLogAdapter) Error(msg string, tags ...tag.Tag) {
	fields, err := l.fields(tags)
	l.logger.Error().Fields(fields).Err(err).Msg(msg)
}

// Fatal implements log.Logger.
func (l *temporalLogAdapter) Fatal(msg string, tags ...tag.Tag) {
	fields, err := l.fields(tags)
	l.logger.Fatal().Fields(fields).Err(err).Msg(msg)
}

// Info implements log.Logger.
func (l *temporalLogAdapter) Info(msg string, tags ...tag.Tag) {
	fields, err := l.fields(tags)
	l.logger.Info().Fields(fields).Err(err).Msg(msg)
}

// Panic implements log.Logger.
func (l *temporalLogAdapter) Panic(msg string, tags ...tag.Tag) {
	fields, err := l.fields(tags)
	l.logger.Panic().Fields(fields).Err(err).Msg(msg)
}

// DPanic implements log.Logger.
func (l *temporalLogAdapter) DPanic(msg string, tags ...tag.Tag) {
	// TODO(michael): Handle development mode panic?
	fields, err := l.fields(tags)
	l.logger.Panic().Fields(fields).Err(err).Msg(msg)
}

// Warn implements log.Logger.
func (l *temporalLogAdapter) Warn(msg string, tags ...tag.Tag) {
	fields, err := l.fields(tags)
	l.logger.Warn().Fields(fields).Err(err).Msg(msg)
}

// With implements log.WithLogger.
func (l *temporalLogAdapter) With(tags ...tag.Tag) temporallog.Logger {
	fields, err := l.fields(tags)
	logger := l.logger.With().Fields(fields).Err(err).Logger()

	return &temporalLogAdapter{
		logger: logger,
	}
}

// Skip implements log.SkipLogger.
func (l *temporalLogAdapter) Skip(extraSkip int) temporallog.Logger {
	logger := l.logger.With().CallerWithSkipFrameCount(extraSkip).Logger()

	return &temporalLogAdapter{
		logger: logger,
	}
}
