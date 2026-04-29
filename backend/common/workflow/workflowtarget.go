//go:generate -command enumer go tool enumer -text -json -yaml -sql -gqlgen -typederrors

package workflow

// WorkflowTarget identifies a client surface a workflow targets.
// Stored as a KeywordList in the pyck_workflow_targets Temporal search attribute,
// so a workflow may be assigned to one or more targets. The string form matches
// the GraphQL WorkflowTarget enum (uppercase) so values round-trip cleanly
// between Go, GraphQL, and Temporal visibility queries.
//
//go:generate enumer -output=workflowtarget_gen.go -type=WorkflowTarget -trimprefix=WorkflowTarget
type WorkflowTarget uint

const (
	WorkflowTargetUNKNOWN WorkflowTarget = iota
	WorkflowTargetWEB
	WorkflowTargetMOBILE
	WorkflowTargetSETUP
)
