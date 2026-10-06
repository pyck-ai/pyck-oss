//go:generate -command enumer go tool enumer -text -json -yaml -sql -gqlgen -typederrors

package feature

//go:generate enumer -output=feature_gen.go -type=Feature -linecomment
type Feature uint

const (
	FEATURE_SHOW_DELETED Feature = iota + 1 // showdeleted
	FEATURE_SYNC_UPDATES                    // syncupdates
	// Deprecated: accepted and ignored so clients that still send it keep working; mutation events are always fire-and-forget now.
	FEATURE_ASYNC_SIGNALS   // asyncsignals
	FEATURE_SUPPRESS_EVENTS // suppressevents
)
