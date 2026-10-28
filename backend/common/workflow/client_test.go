package workflow_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"

	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/common/workflow"
)

func TestValidateWorkflowID(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		id    string
		valid bool
	}{
		{"plain id", "PickingWorkflow_0198a6b2-0000-7000-8000-000000000001", true},
		{"max length", strings.Repeat("a", workflow.MaxWorkflowIDLength), true},
		{"empty", "", false},
		{"over max length", strings.Repeat("a", workflow.MaxWorkflowIDLength+1), false},
		{"leading NUL", "\x00probe", false},
		{"inner NUL", "probe\x00id", false},
		{"trailing NUL", "probe\x00", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := workflow.ValidateWorkflowID(tc.id)
			if tc.valid {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, workflow.ErrInvalidWorkflowID)
			}
		})
	}
}

func TestValidateUpdateName(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		updateName string
		valid      bool
	}{
		{"plain name", "ConfirmPick", true},
		{"generic type name", "Confirm[example.com/pkg.Foo]", true},
		{"max length", strings.Repeat("a", workflow.MaxWorkflowIDLength), true},
		{"empty", "", false},
		{"blank", " \t ", false},
		{"over max length", strings.Repeat("a", workflow.MaxWorkflowIDLength+1), false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := workflow.ValidateUpdateName(tc.updateName)
			if tc.valid {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, workflow.ErrInvalidUpdateName)
			}
		})
	}
}

func TestClient_RejectsInvalidWorkflowIDBeforeTemporal(t *testing.T) {
	t.Parallel()

	// No Temporal client: reaching Temporal would panic on the nil client.
	c, err := workflow.NewClient("ns", nil)
	require.NoError(t, err)

	calls := map[string]func(ctx context.Context, id string) error{
		"GetWorkflowResult": func(ctx context.Context, id string) error {
			return c.GetWorkflowResult(ctx, id, "run", nil)
		},
		"AwaitWorkflowResult": func(ctx context.Context, id string) error {
			return c.AwaitWorkflowResult(ctx, id, nil)
		},
		"GetWorkflowExecutionInfo": func(ctx context.Context, id string) error {
			_, err := c.GetWorkflowExecutionInfo(ctx, id, "run")
			return err
		},
		"GetWorkflowHistory": func(ctx context.Context, id string) error {
			_, err := c.GetWorkflowHistory(ctx, id, "run")
			return err
		},
		"QueryWorkflow": func(ctx context.Context, id string) error {
			return c.QueryWorkflow(ctx, id, "run", "query", nil, nil)
		},
		"SignalWorkflow": func(ctx context.Context, id string) error {
			return c.SignalWorkflow(ctx, id, "run", "signal", nil)
		},
		"CancelWorkflow": func(ctx context.Context, id string) error {
			return c.CancelWorkflow(ctx, id, "run")
		},
		"UpdateWorkflow": func(ctx context.Context, id string) error {
			return c.UpdateWorkflow(ctx, id, "run", "update", nil, nil)
		},
		"ResolveRemoteUIBundle": func(ctx context.Context, id string) error {
			_, err := c.ResolveRemoteUIBundle(ctx, id, "", nil)
			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.ErrorIs(t, call(t.Context(), "probe\x00id"), workflow.ErrInvalidWorkflowID)
		})
	}
}

type awaitResult struct {
	Value string
}

// A caller that only knows the deterministic workflow ID must be able to
// wait on whatever run currently holds it: the SDK resolves the latest run
// when the run ID is empty, so the handle must be requested with "".
func TestAwaitWorkflowResult_WaitsOnLatestRunAndDecodesResult(t *testing.T) {
	t.Parallel()
	temporalMock := new(mocks.MockTemporalClient)
	run := mocks.NewMockWorkflowRun("register-tenant-1", "run-7", &awaitResult{Value: "done"}, nil)
	temporalMock.On("GetWorkflow", mock.Anything, "register-tenant-1", "").Return(run).Once()
	client, err := workflow.NewClient("default", temporalMock)
	require.NoError(t, err)

	var got awaitResult
	require.NoError(t, client.AwaitWorkflowResult(t.Context(), "register-tenant-1", &got))

	assert.Equal(t, "done", got.Value)
	temporalMock.AssertExpectations(t)
}

// An empty ID would make the SDK wait on nothing in particular; it is
// refused before any Temporal call.
func TestAwaitWorkflowResult_RejectsEmptyWorkflowID(t *testing.T) {
	t.Parallel()
	temporalMock := new(mocks.MockTemporalClient)
	client, err := workflow.NewClient("default", temporalMock)
	require.NoError(t, err)

	err = client.AwaitWorkflowResult(t.Context(), "", nil)

	require.ErrorIs(t, err, workflow.ErrInvalidWorkflowID)
	temporalMock.AssertNotCalled(t, "GetWorkflow", mock.Anything, mock.Anything, mock.Anything)
}

// An execution that does not exist yet surfaces as the SDK's NotFound so
// the caller can decide to retry while somebody else starts it.
func TestAwaitWorkflowResult_SurfacesNotFoundUnwrapped(t *testing.T) {
	t.Parallel()
	temporalMock := new(mocks.MockTemporalClient)
	run := mocks.NewMockWorkflowRun("register-tenant-1", "", nil, serviceerror.NewNotFound("workflow not found"))
	temporalMock.On("GetWorkflow", mock.Anything, "register-tenant-1", "").Return(run).Once()
	client, err := workflow.NewClient("default", temporalMock)
	require.NoError(t, err)

	err = client.AwaitWorkflowResult(t.Context(), "register-tenant-1", nil)

	var notFound *serviceerror.NotFound
	require.ErrorAs(t, err, &notFound, "NotFound must reach the caller as-is")
	temporalMock.AssertExpectations(t)
}
