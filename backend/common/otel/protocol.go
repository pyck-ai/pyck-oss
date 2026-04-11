//go:generate -command enumer go tool enumer -text -json -yaml -sql -gqlgen -typederrors

package otel

// ProtocolType defines the OTLP protocol for exporting traces.
//
//go:generate enumer -output=protocol_gen.go -type=ProtocolType -linecomment
type ProtocolType uint

const (
	ProtocolTypeInvalid      ProtocolType = iota // invalid
	ProtocolTypeGRPC                             // grpc
	ProtocolTypeHTTP                             // http
	ProtocolTypeHTTPProtobuf                     // http/protobuf
)
