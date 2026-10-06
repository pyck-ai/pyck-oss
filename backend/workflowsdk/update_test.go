package workflowsdk_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	common_workflow "github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/workflowsdk"
)

// gatedInput drives the fake updater: RejectInValidator makes Validate fail,
// FailInUpdate makes Update fail, neither lets the update through.
type gatedInput struct {
	RejectInValidator bool `json:"rejectInValidator"`
	FailInUpdate      bool `json:"failInUpdate"`
}

type gatedUpdate struct {
	workflowsdk.WorkflowUpdate[gatedInput, struct{}]
}

var (
	errValidatorSaysNo = errors.New("validator says no")
	errUpdateSaysNo    = errors.New("update says no")
)

func (u *gatedUpdate) Type() *common_workflow.WorkflowUpdateType {
	return u.DefaultType(u)
}

func (u *gatedUpdate) Await(ctx workflow.Context, input *common_workflow.UserDataInput, ref struct{}) error {
	return u.DefaultAwait(ctx, u, input, ref)
}

func (u *gatedUpdate) Validate(_ workflow.Context, _ struct{}, value gatedInput) error {
	if value.RejectInValidator {
		return errValidatorSaysNo
	}

	return nil
}

func (u *gatedUpdate) Update(ctx workflow.Context, input *common_workflow.UserDataInput, _ struct{}, value gatedInput) (any, error) {
	if value.FailInUpdate {
		return nil, errUpdateSaysNo
	}

	return u.DefaultUpdate(ctx, u, input, value)
}

func gatedWorkflow(ctx workflow.Context) error {
	var (
		u     gatedUpdate
		input common_workflow.UserDataInput
	)

	return u.Await(ctx, &input, struct{}{})
}

// recordingLogger keeps every Warn line so the test can assert on what the
// update handler logged.
type recordingLogger struct {
	mu    sync.Mutex
	warns []string
}

func (l *recordingLogger) Debug(string, ...any) {}
func (l *recordingLogger) Info(string, ...any)  {}
func (l *recordingLogger) Error(string, ...any) {}

func (l *recordingLogger) Warn(msg string, keyvals ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprint(append([]any{msg}, keyvals...)...))
}

func (l *recordingLogger) Warns() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.warns...)
}

var _ log.Logger = (*recordingLogger)(nil)

// updateOutcome collects the callbacks of one UpdateWorkflow call.
type updateOutcome struct {
	rejected  error
	completed error
}

func (o *updateOutcome) Accept()                   {}
func (o *updateOutcome) Reject(err error)          { o.rejected = err }
func (o *updateOutcome) Complete(_ any, err error) { o.completed = err }

func TestDefaultAwait_LogsRejections(t *testing.T) {
	t.Parallel()

	logger := &recordingLogger{}

	var suite testsuite.WorkflowTestSuite
	suite.SetLogger(logger)
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(gatedWorkflow)

	updateType := (&gatedUpdate{}).Type().ID

	var byValidator, byUpdate, accepted updateOutcome

	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(updateType, "rejected-by-validator", &byValidator, gatedInput{RejectInValidator: true})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(updateType, "rejected-by-update", &byUpdate, gatedInput{FailInUpdate: true})
	}, 2*time.Second)
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(updateType, "accepted", &accepted, gatedInput{})
	}, 3*time.Second)

	env.ExecuteWorkflow(gatedWorkflow)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	require.ErrorContains(t, byValidator.rejected, errValidatorSaysNo.Error())
	require.ErrorContains(t, byUpdate.completed, errUpdateSaysNo.Error())
	require.NoError(t, accepted.rejected)
	require.NoError(t, accepted.completed)

	warns := logger.Warns()
	require.Len(t, warns, 2, "one Warn per rejection, none for the accepted update: %v", warns)

	require.Contains(t, warns[0], "update rejected by validator")
	require.Contains(t, warns[0], updateType)
	require.Contains(t, warns[0], "rejected-by-validator")
	require.Contains(t, warns[0], errValidatorSaysNo.Error())

	require.Contains(t, warns[1], "update rejected")
	require.NotContains(t, warns[1], "by validator")
	require.Contains(t, warns[1], updateType)
	require.Contains(t, warns[1], "rejected-by-update")
	require.Contains(t, warns[1], errUpdateSaysNo.Error())
}
