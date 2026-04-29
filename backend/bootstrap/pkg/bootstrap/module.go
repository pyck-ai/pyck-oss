//go:generate -command enumer go tool enumer -text -json -yaml -sql -gqlgen -typederrors
package bootstrap

// BootstrapModule identifies a bootstrap target.
//
//go:generate enumer -output=module_gen.go -type=BootstrapModule -linecomment
type BootstrapModule uint

const (
	BootstrapModuleZitadel  BootstrapModule = 1 + iota // zitadel
	BootstrapModuleTemporal                            // temporal
	BootstrapModuleMinio                               // minio
)
