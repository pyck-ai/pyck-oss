//go:generate -command enumer go tool enumer -text -json -yaml -sql -gqlgen -typederrors

package config

// AdapterType defines the type of event adapter to use.
//
//go:generate enumer -output=adapter_gen.go -type=AdapterType -linecomment
type AdapterType uint

const (
	AdapterTypeInvalid        AdapterType = iota // invalid
	AdapterTypeDefault                           // default
	AdapterTypeGRPC                              // grpc
	AdapterTypePostgresListen                    // postgres_listen
)
