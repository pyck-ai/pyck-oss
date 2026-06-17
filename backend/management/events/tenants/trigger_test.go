//nolint:testpackage // handleTriggerMessage is intentionally unexported; tests live alongside.
package tenants

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	temporalclient "go.temporal.io/sdk/client"
	temporalmocks "go.temporal.io/sdk/mocks"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"

	"github.com/pyck-ai/pyck/backend/management/workflows"
)

// TestHandleTriggerMessage_TerminatesExistingOnConflict is the M3
// regression test for PR #1172. Originally the trigger used
// WORKFLOW_ID_CONFLICT_POLICY_FAIL with an ACK-on-AlreadyStarted
// branch, which silently dropped opposite-direction transitions
// (a restore event arriving while DisableTenantWorkflow was still
// in-flight got ACKed but never effective until tenant-reconcile
// healed the drift ~minutes later). The fix routes the
// "latest-user-intent-wins" semantics through Temporal's built-in
// TERMINATE_EXISTING policy instead.
//
// This test pins the policy choice so the fix can't silently
// regress to FAIL by a copy-paste from tenant-reconcile (which
// correctly keeps FAIL — it's a healing loop, not a user-intent
// dispatcher).
func TestHandleTriggerMessage_TerminatesExistingOnConflict(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		deletedBefore any
		deletedAfter  any
		wantWorkflow  string
	}{
		{
			name:          "disable transition",
			deletedBefore: nil,
			deletedAfter:  time.Now().UTC().Format(time.RFC3339Nano),
			wantWorkflow:  workflows.DisableTenantWorkflow,
		},
		{
			name:          "restore transition",
			deletedBefore: time.Now().UTC().Format(time.RFC3339Nano),
			deletedAfter:  nil,
			wantWorkflow:  workflows.RestoreTenantWorkflow,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tenantID := uuid.New()
			idpOrgRef := "375900000000000001"

			payload := events.MutationEventMessage{
				Service:   "management",
				Schema:    "tenant",
				Operation: events.OpUpdate,
				ID:        tenantID,
				TenantID:  tenantID,
				DataBefore: map[string]any{
					"deleted_at":  tc.deletedBefore,
					"idp_org_ref": idpOrgRef,
				},
				DataAfter: map[string]any{
					"deleted_at":  tc.deletedAfter,
					"idp_org_ref": idpOrgRef,
				},
			}
			data, err := json.Marshal(payload)
			require.NoError(t, err)

			msg := newMsg(t, data)

			mockClient := new(mocks.MockTemporalClient)
			mockRun := new(temporalmocks.WorkflowRun)
			mockRun.On("GetRunID").Return("test-run-id")

			var capturedOpts temporalclient.StartWorkflowOptions
			mockClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
				Run(func(args mock.Arguments) {
					capturedOpts = args.Get(1).(temporalclient.StartWorkflowOptions)
				}).
				Return(mockRun, nil).
				Once()

			handleTriggerMessage(context.Background(), msg, mockClient)

			require.True(t, msg.IsAcked(), "expected message ACK after successful workflow start")
			assert.False(t, msg.IsNaked(), "must not NAK on success")

			assert.Equal(t,
				enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
				capturedOpts.WorkflowIDConflictPolicy,
				"trigger must use TERMINATE_EXISTING so opposite-direction "+
					"events kill the in-flight workflow instead of being "+
					"ACK-dropped (M3 regression)",
			)
			assert.Equal(t,
				enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
				capturedOpts.WorkflowIDReusePolicy,
				"reuse policy unchanged — completed workflow's ID can be reused",
			)
			assert.Equal(t,
				workflows.TenantLifecycleWorkflowID(tenantID),
				capturedOpts.ID,
				"shared workflow ID is the serialization slot — preserved",
			)

			mockClient.AssertExpectations(t)
		})
	}
}

// TestHandleTriggerMessage_NakOnTemporalError confirms the
// non-AlreadyStarted failure path still NAKs so JetStream
// redelivers (tenant-reconcile is the longer-window safety net).
// AlreadyStarted is structurally unreachable with
// TERMINATE_EXISTING but we keep the NAK branch for real
// transport faults.
func TestHandleTriggerMessage_NakOnTemporalError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	idpOrgRef := "375900000000000002"

	payload := events.MutationEventMessage{
		Service:   "management",
		Schema:    "tenant",
		Operation: events.OpDelete,
		ID:        tenantID,
		TenantID:  tenantID,
		DataBefore: map[string]any{
			"deleted_at":  nil,
			"idp_org_ref": idpOrgRef,
		},
		DataAfter: map[string]any{
			"deleted_at":  time.Now().UTC().Format(time.RFC3339Nano),
			"idp_org_ref": idpOrgRef,
		},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	msg := newMsg(t, data)

	mockClient := new(mocks.MockTemporalClient)
	mockRun := new(temporalmocks.WorkflowRun)
	mockClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(mockRun, errors.New("temporal unreachable")).
		Once()

	handleTriggerMessage(context.Background(), msg, mockClient)

	assert.True(t, msg.IsNaked(), "expected NAK on transport error so JetStream redelivers")
	assert.False(t, msg.IsAcked(), "must not ACK on error — drift would persist")
	mockClient.AssertExpectations(t)
}

// newMsg constructs a [mocks.JetstreamMsg] with its internal done
// channel pre-initialised. The mock's init() is package-private and
// only runs from Wait(), so synchronous tests that drive Ack/Nak
// without a paired Wait() goroutine would otherwise hit
// "close of nil channel" when the handler signals. Calling Wait
// with an already-cancelled context triggers init() and returns
// immediately, leaving Ack/Nak safe to invoke.
func newMsg(t *testing.T, data []byte) *mocks.JetstreamMsg {
	t.Helper()
	msg := &mocks.JetstreamMsg{DataByte: data}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_ = msg.Wait(cancelled)
	return msg
}
