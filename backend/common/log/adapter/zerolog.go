package adapter

import (
	"strings"

	"github.com/pyck-ai/pyck/backend/common/log"
)

type zerologWriter struct {
	logger log.Logger
}

func (w *zerologWriter) Write(p []byte) (int, error) {
	// Trim trailing newlines as zerolog adds its own
	msg := strings.TrimSuffix(string(p), "\n")
	w.logger.Error().Msg(msg)
	return len(p), nil
}
