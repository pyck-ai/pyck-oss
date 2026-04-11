package adapter

import (
	"fmt"

	"github.com/pyck-ai/pyck/backend/common/log"
)

const entLogSkipFrames = 3

// EntLogAdapter adapts a zerolog logger to an ent logger.
func EntLogAdapter(zl log.Logger) func(...any) {
	// TODO(michael): Add support for context to pass request IDs etc.

	logger := zl.With().
		Str("log.adapter", "ent").
		Logger()

	return func(a ...any) {
		logger.Debug().
			CallerSkipFrame(entLogSkipFrames).
			Msg(fmt.Sprint(a...))
	}
}
