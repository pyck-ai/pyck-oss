//go:generate -command enumer go tool enumer -text -json -yaml -sql -gqlgen -typederrors

package authn

// Role represents user authorization levels.
// Higher values indicate higher privileges: SYSTEM > ADMIN > WRITER > READER > NONE
//
//go:generate enumer -output=role_gen.go -type=Role -linecomment
type Role uint

const (
	ROLE_NONE   Role = iota // none
	ROLE_READER             // reader
	ROLE_WRITER             // writer
	ROLE_ADMIN              // admin
	ROLE_SYSTEM             // system
)
