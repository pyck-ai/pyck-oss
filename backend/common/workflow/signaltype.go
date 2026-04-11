//go:generate -command enumer go tool enumer -text -json -yaml -sql -gqlgen -typederrors

package workflow

// TemporalSignalType values.
//
//go:generate enumer -output=signaltype_gen.go -type=SignalType -linecomment
type SignalType uint

const (
	SIGNAL_UNKNOWN      SignalType = iota // unknown
	SIGNAL_START                          // start
	SIGNAL_INTERMEDIATE                   // intermediate
)
