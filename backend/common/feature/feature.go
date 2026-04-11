//go:generate -command enumer go tool enumer -text -json -yaml -sql -gqlgen -typederrors

package feature

//go:generate enumer -output=feature_gen.go -type=Feature -linecomment
type Feature uint

const (
	FEATURE_SHOW_DELETED  Feature = iota + 1 // showdeleted
	FEATURE_SYNC_UPDATES                     // syncupdates
	FEATURE_ASYNC_SIGNALS                    // asyncsignals
)
