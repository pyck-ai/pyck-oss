package workflow_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestClient_RejectsInvalidWorkflowIDBeforeTemporal(t *testing.T) {
	t.Parallel()

	// No Temporal client: reaching Temporal would panic on the nil client.
	c, err := workflow.NewClient("ns", nil)
	require.NoError(t, err)

	calls := map[string]func(ctx context.Context, id string) error{
		"GetWorkflowResult": func(ctx context.Context, id string) error {
			return c.GetWorkflowResult(ctx, id, "run", nil)
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
