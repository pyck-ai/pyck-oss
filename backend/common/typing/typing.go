package typing

import "github.com/rs/zerolog"

type LogLevel string

func (ll LogLevel) String() string {
	return string(ll)
}

func (ll LogLevel) AsZeroLogLevel() zerolog.Level {
	switch ll {
	case "debug":
		return zerolog.DebugLevel
	case "info":
		return zerolog.InfoLevel
	case "warn":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	case "fatal":
		return zerolog.FatalLevel
	case "panic":
		return zerolog.PanicLevel
	default:
		return zerolog.InfoLevel
	}
}
