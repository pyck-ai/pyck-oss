package util

import (
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var logger zerolog.Logger

func init() {
	// Set log level based on TEST_LOG_LEVEL environment variable
	// Default to "info" if not set
	logLevel := os.Getenv("TEST_LOG_LEVEL")
	level := zerolog.InfoLevel
	if logLevel != "" {
		parsedLevel, err := zerolog.ParseLevel(logLevel)
		if err == nil {
			level = parsedLevel
		}
	}

	// Configure logger
	logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: "15:04:05",
	}).Level(level)
}

// Debug logs a debug message
func logDebug(msg string, args ...interface{}) {
	logger.Debug().Msgf(msg, args...)
}

// Info logs an info message
func LogInfo(msg string, args ...interface{}) {
	logger.Info().Msgf(msg, args...)
}

// Error logs an error message
func logError(msg string, err error, args ...interface{}) {
	logger.Error().Err(err).Msgf(msg, args...)
}
