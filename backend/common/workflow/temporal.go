//go:generate -command enumer go tool enumer -text -json -yaml -sql -gqlgen -typederrors

package workflow

//go:generate enumer -output=temporal_gen.go -type=WorkflowQueryType -trimprefix=WorkflowQueryType
type WorkflowQueryType uint

const (
	WorkflowQueryTypeGetState WorkflowQueryType = iota + 1
	WorkflowQueryTypeGetUserDataInput
	WorkflowQueryTypeAwaitUserDataInput
	WorkflowQueryTypeGetAssignee
	WorkflowQueryTypeSetAssignee
	WorkflowQueryTypeGetAvailableActions
	WorkflowQueryTypeGetTargets
	WorkflowQueryTypeSetTargets
	WorkflowQueryTypeGetIsAssignable
	WorkflowQueryTypeSetIsAssignable
)
