package adapter

import (
	stdlog "log"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// StdLogAdapter adapts a zerolog logger to a standard library logger.
func StdLogAdapter(zl log.Logger) *stdlog.Logger {
	logger := zl.With().
		Str("log.adapter", "std").
		Logger()

	return stdlog.New(
		&zerologWriter{
			logger: logger,
		},
		"", // No prefix
		0,  // No flags, we handle timestamp and caller in the logger setup
	)
}
