//nolint:testpackage // handleTriggerMessage is intentionally unexported; tests live alongside.
package tenants

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	temporalclient "go.temporal.io/sdk/client"
	temporalmocks "go.temporal.io/sdk/mocks"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/events/topic"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"

	"github.com/pyck-ai/pyck/backend/management/workflows"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

// testCfg is the trigger configuration the handler tests run with: the
// bounds main builds from the env config, whose defaults these values
// mirror (PYCK_TENANT_EVENT_MAX_REDELIVER, PYCK_TENANT_EVENT_TEMPORAL_CALL_TIMEOUT).
func testCfg() TriggerConfig {
	return TriggerConfig{MaxRedeliver: 5, TemporalCallTimeout: 10 * time.Second}
}

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

			handleTriggerMessage(t.Context(), msg, mockClient, testCfg())

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

	handleTriggerMessage(t.Context(), msg, mockClient, testCfg())

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

// =============================================================================
// Registration: create event of a pending row → workflow start
// =============================================================================

// tenantEventMsg marshals a tenant mutation event the way the outbox
// drainer publishes it. dataBefore may be nil (create events have none).
func tenantEventMsg(t *testing.T, op string, tenantID uuid.UUID, dataBefore, dataAfter map[string]any) *mocks.JetstreamMsg {
	t.Helper()
	payload := events.MutationEventMessage{
		Service:   "management",
		Schema:    "tenant",
		Operation: op,
		ID:        tenantID,
		TenantID:  tenantID,
		DataAfter: dataAfter,
	}
	if dataBefore != nil {
		payload.DataBefore = dataBefore
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	return newMsg(t, data)
}

func pendingRow(tenantID uuid.UUID) map[string]any {
	return map[string]any{
		"status":      "pending",
		"idp_org_ref": tenantID.String(),
		"deleted_at":  nil,
	}
}

// The create event is the only thing that starts a registration: the
// start must target the deterministic ID the resolver waits on, hand the
// workflow nothing but the tenant ID (everything else, the password hash
// included, stays on the row) and leave a retry after a failed run
// possible.
func TestHandleTriggerMessage_StartsRegistrationOnPendingCreate(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	msg := tenantEventMsg(t, events.OpCreate, tenantID, nil, pendingRow(tenantID))

	mockClient := new(mocks.MockTemporalClient)
	mockRun := new(temporalmocks.WorkflowRun)
	mockRun.On("GetRunID").Return("run-1")
	var capturedOpts temporalclient.StartWorkflowOptions
	var capturedArgs []interface{}
	mockClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, workflows.RegisterTenantWorkflow, mock.Anything).
		Run(func(args mock.Arguments) {
			var ok bool
			capturedOpts, ok = args.Get(1).(temporalclient.StartWorkflowOptions)
			require.True(t, ok, "start options type")
			capturedArgs, ok = args.Get(3).([]interface{})
			require.True(t, ok, "workflow args type")
		}).
		Return(mockRun, nil).Once()

	handleTriggerMessage(t.Context(), msg, mockClient, testCfg())

	assert.True(t, msg.IsAcked(), "started → ACK")
	assert.False(t, msg.IsNaked())
	mockClient.AssertExpectations(t)
	assert.Equal(t, workflows.RegisterTenantWorkflowID(tenantID), capturedOpts.ID)
	assert.Equal(t, workflows.TemporalManagementTaskQueue, capturedOpts.TaskQueue)
	assert.Equal(t, enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY, capturedOpts.WorkflowIDReusePolicy,
		"a failed registration must stay retryable under the same ID")
	assert.Equal(t, registertenant.ExecutionTimeout, capturedOpts.WorkflowExecutionTimeout,
		"a run that cannot make progress must close instead of holding the ID and the pending row forever")
	require.Len(t, capturedArgs, 1)
	assert.Equal(t, registertenant.RegisterTenantWorkflowInput{TenantID: tenantID}, capturedArgs[0])
}

// Temporal being unreachable is transient: the message is NAKed and the
// redelivery starts the run.
func TestHandleTriggerMessage_NaksWhenRegistrationStartFails(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	msg := tenantEventMsg(t, events.OpCreate, tenantID, nil, pendingRow(tenantID))

	mockClient := new(mocks.MockTemporalClient)
	mockRun := new(temporalmocks.WorkflowRun)
	mockClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(mockRun, errors.New("connection refused")).Once()

	handleTriggerMessage(t.Context(), msg, mockClient, testCfg())

	assert.True(t, msg.IsNaked(), "start failure must be redelivered")
	assert.False(t, msg.IsAcked(), "ACK would leave the row pending forever")
	mockClient.AssertExpectations(t)
}

// JetStream drops a message silently once it has been delivered
// MaxRedeliver times. A start failure on that last delivery is the last
// trace of the registration, so it must be an Error line naming the
// drop; earlier failures are ordinary NAKs.
func TestHandleTriggerMessage_LogsDroppedCreateEvent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		deliveries uint64
		dropped    bool
	}{
		{"first delivery is only redelivered", 1, false},
		{"last permitted delivery is logged as dropped", uint64(testCfg().MaxRedeliver), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tenantID := uuid.New()
			msg := tenantEventMsg(t, events.OpCreate, tenantID, nil, pendingRow(tenantID))
			msg.Deliveries = tc.deliveries

			var logs bytes.Buffer
			logger := log.ForContext(t.Context()).Output(&logs).Level(log.DebugLevel)
			ctx := log.Context(t.Context(), logger)

			mockClient := new(mocks.MockTemporalClient)
			mockRun := new(temporalmocks.WorkflowRun)
			mockClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
				Return(mockRun, errors.New("connection refused")).Once()

			handleTriggerMessage(ctx, msg, mockClient, testCfg())

			assert.True(t, msg.IsNaked())
			assert.False(t, msg.IsAcked())
			if tc.dropped {
				assert.Contains(t, logs.String(), "exhausted its redeliveries")
				assert.Contains(t, logs.String(), `"level":"error"`)
			} else {
				assert.NotContains(t, logs.String(), "exhausted its redeliveries")
			}
		})
	}
}

// A redelivered create event finds the run already started (or already
// completed under the reuse policy); no redelivery can change that.
func TestHandleTriggerMessage_AcksWhenRegistrationAlreadyStarted(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	msg := tenantEventMsg(t, events.OpCreate, tenantID, nil, pendingRow(tenantID))

	mockClient := new(mocks.MockTemporalClient)
	mockRun := new(temporalmocks.WorkflowRun)
	mockClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(mockRun, serviceerror.NewWorkflowExecutionAlreadyStarted("already started", "", "")).Once()

	handleTriggerMessage(t.Context(), msg, mockClient, testCfg())

	assert.True(t, msg.IsAcked(), "the same operation is already in flight")
	assert.False(t, msg.IsNaked())
	mockClient.AssertExpectations(t)
}

// A create event that is not a pending registration (rows created by an
// older build, or by a manual insert already marked done) has nothing to
// provision. Starting a run would only be a no-op; the event is dropped.
func TestHandleTriggerMessage_IgnoresCreateWithoutPendingStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		dataAfter map[string]any
	}{
		{"status done", map[string]any{"status": "done", "idp_org_ref": "org-1"}},
		{"status error", map[string]any{"status": "error", "idp_org_ref": "org-1"}},
		{"status missing", map[string]any{"idp_org_ref": "org-1"}},
		{"status not a string", map[string]any{"status": 1, "idp_org_ref": "org-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			msg := tenantEventMsg(t, events.OpCreate, uuid.New(), nil, tc.dataAfter)
			mockClient := new(mocks.MockTemporalClient)

			handleTriggerMessage(t.Context(), msg, mockClient, testCfg())

			assert.True(t, msg.IsAcked())
			assert.False(t, msg.IsNaked())
			mockClient.AssertNotCalled(t, "ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// A pending row may already own a Zitadel org: the registration run
// creates it before the final status flip, and the resolver may have
// answered "outcome unknown" while the run still provisions. Retiring or
// restoring such a row must dispatch the lifecycle workflow like any other
// row: a skipped delete would leave the org ACTIVE with a soft-deleted
// row and nothing that ever corrects it (tenant-reconcile ignores pending
// tombstones). Both lifecycle workflows tolerate an org that was never
// created.
func TestHandleTriggerMessage_DispatchesLifecycleForPendingRows(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	cases := []struct {
		name          string
		op            string
		deletedBefore any
		deletedAfter  any
		wantWorkflow  string
	}{
		{"soft-delete of a pending row", events.OpDelete, nil, now, workflows.DisableTenantWorkflow},
		{"restore of a pending row", events.OpUpdate, now, nil, workflows.RestoreTenantWorkflow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tenantID := uuid.New()
			before := pendingRow(tenantID)
			before["deleted_at"] = tc.deletedBefore
			after := pendingRow(tenantID)
			after["deleted_at"] = tc.deletedAfter
			msg := tenantEventMsg(t, tc.op, tenantID, before, after)

			mockClient := new(mocks.MockTemporalClient)
			mockRun := new(temporalmocks.WorkflowRun)
			mockRun.On("GetRunID").Return("run-1")
			var capturedOpts temporalclient.StartWorkflowOptions
			mockClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, tc.wantWorkflow, mock.Anything).
				Run(func(args mock.Arguments) {
					var ok bool
					capturedOpts, ok = args.Get(1).(temporalclient.StartWorkflowOptions)
					require.True(t, ok, "start options type")
				}).
				Return(mockRun, nil).Once()

			handleTriggerMessage(t.Context(), msg, mockClient, testCfg())

			assert.True(t, msg.IsAcked())
			assert.False(t, msg.IsNaked())
			mockClient.AssertExpectations(t)
			assert.Equal(t, workflows.TenantLifecycleWorkflowID(tenantID), capturedOpts.ID)
		})
	}
}

// An error row (a registration that failed after its org was created)
// is retired with deleteTenant like a done tenant: its delete event must
// dispatch DisableTenantWorkflow, whose Zitadel step tolerates an org
// that was never created.
func TestHandleTriggerMessage_DispatchesDisableForErrorRows(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	before := map[string]any{"status": "error", "idp_org_ref": tenantID.String(), "deleted_at": nil}
	after := map[string]any{"status": "error", "idp_org_ref": tenantID.String(), "deleted_at": time.Now().UTC().Format(time.RFC3339Nano)}
	msg := tenantEventMsg(t, events.OpDelete, tenantID, before, after)

	mockClient := new(mocks.MockTemporalClient)
	mockRun := new(temporalmocks.WorkflowRun)
	mockRun.On("GetRunID").Return("run-1")
	var capturedOpts temporalclient.StartWorkflowOptions
	mockClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, workflows.DisableTenantWorkflow, mock.Anything).
		Run(func(args mock.Arguments) {
			var ok bool
			capturedOpts, ok = args.Get(1).(temporalclient.StartWorkflowOptions)
			require.True(t, ok, "start options type")
		}).
		Return(mockRun, nil).Once()

	handleTriggerMessage(t.Context(), msg, mockClient, testCfg())

	assert.True(t, msg.IsAcked())
	assert.False(t, msg.IsNaked())
	mockClient.AssertExpectations(t)
	assert.Equal(t, workflows.TenantLifecycleWorkflowID(tenantID), capturedOpts.ID)
}

// The registration status flip (pending → done or → error) reaches this
// consumer as a tenant update event carrying the row: deleted_at is the
// zero-time string on both sides, so no lifecycle transition happened
// and no workflow may be dispatched. Dispatching one here would
// deactivate the Zitadel org of the tenant that just finished
// registering.
func TestHandleTriggerMessage_IgnoresRegistrationStatusFlip(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"done", "error"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()

			tenantID := uuid.New()
			before := map[string]any{"status": "pending", "idp_org_ref": tenantID.String(), "deleted_at": topic.ZeroTimeStr}
			after := map[string]any{"status": status, "idp_org_ref": tenantID.String(), "deleted_at": topic.ZeroTimeStr}
			msg := tenantEventMsg(t, events.OpUpdate, tenantID, before, after)

			mockClient := new(mocks.MockTemporalClient)

			handleTriggerMessage(t.Context(), msg, mockClient, testCfg())

			assert.True(t, msg.IsAcked(), "a settled flip event is fully handled")
			assert.False(t, msg.IsNaked(), "a redelivery cannot change the verdict")
			mockClient.AssertNotCalled(t, "ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// Malformed or foreign messages can never succeed on redelivery; they are
// ACKed (dropped) so they do not poison the consumer.
func TestHandleTriggerMessage_AcksUnprocessableMessages(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	foreign := events.MutationEventMessage{Service: "inventory", Schema: "item", Operation: events.OpCreate, ID: tenantID, DataAfter: pendingRow(tenantID)}
	foreignData, err := json.Marshal(foreign)
	require.NoError(t, err)
	nilID := events.MutationEventMessage{Service: "management", Schema: "tenant", Operation: events.OpCreate, DataAfter: pendingRow(tenantID)}
	nilIDData, err := json.Marshal(nilID)
	require.NoError(t, err)
	nonMap := events.MutationEventMessage{Service: "management", Schema: "tenant", Operation: events.OpDelete, ID: tenantID, DataAfter: "hard-deleted"}
	nonMapData, err := json.Marshal(nonMap)
	require.NoError(t, err)

	cases := []struct {
		name string
		data []byte
	}{
		{"not json", []byte("{not-json")},
		{"other service", foreignData},
		{"nil tenant id", nilIDData},
		{"DataAfter not a map (hard delete)", nonMapData},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			msg := newMsg(t, tc.data)
			mockClient := new(mocks.MockTemporalClient)

			handleTriggerMessage(t.Context(), msg, mockClient, testCfg())

			assert.True(t, msg.IsAcked())
			assert.False(t, msg.IsNaked())
			mockClient.AssertNotCalled(t, "ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// The durable consumer only ever sees the subjects it filters on. The
// pending row's create event is the sole thing that starts a
// RegisterTenantWorkflow, so dropping it from the filter leaves every
// registration pending while the update/delete lifecycle keeps working;
// nothing else would notice.
func TestTriggerSubjects_IncludeTenantCreate(t *testing.T) {
	t.Parallel()

	want := make([]string, 0, 3)
	for _, op := range []string{events.OpCreate, events.OpUpdate, events.OpDelete} {
		want = append(want, events.MutationEventTopic{
			StreamName:    "s",
			ServiceName:   topic.ManagementService,
			SchemaName:    topic.TenantSchema,
			OperationName: op,
		}.String())
	}
	require.ElementsMatch(t, want, triggerSubjects("s"))
}

// A plain NAK is redelivered by nats-server at once (the consumer's
// BackOff only paces ACK-timeout redeliveries) so a short Temporal outage
// would burn every delivery within seconds and drop the create event. Each
// failed delivery must therefore NAK with the delay of its own position in
// the schedule, growing with the delivery count and clamped at the last
// entry, and never with a zero (plain) NAK.
func TestHandleTriggerMessage_NaksWithGrowingDelay(t *testing.T) {
	t.Parallel()

	// A cap above the schedule length, so the clamp is reached without the
	// last delivery being the dropped one.
	cfg := TriggerConfig{MaxRedeliver: 10, TemporalCallTimeout: 10 * time.Second}
	want := map[uint64]time.Duration{
		0: 2 * time.Second, // no delivery count: the first delay
		1: 2 * time.Second,
		2: 5 * time.Second,
		3: 15 * time.Second,
		4: 30 * time.Second,
		5: 60 * time.Second,
		9: 60 * time.Second, // past the schedule: the last entry repeats
	}
	for deliveries, delay := range want {
		t.Run(fmt.Sprintf("delivery %d", deliveries), func(t *testing.T) {
			t.Parallel()

			tenantID := uuid.New()
			msg := tenantEventMsg(t, events.OpCreate, tenantID, nil, pendingRow(tenantID))
			msg.Deliveries = deliveries

			mockClient := new(mocks.MockTemporalClient)
			mockClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
				Return(new(temporalmocks.WorkflowRun), errors.New("connection refused")).Once()

			handleTriggerMessage(t.Context(), msg, mockClient, cfg)

			assert.False(t, msg.IsAcked())
			assert.Equal(t, []time.Duration{delay}, msg.NakDelays(),
				"delivery %d must be NAKed once with its schedule delay", deliveries)
		})
	}
}

// The delays across one message's deliveries grow strictly until the
// schedule ends: the same message redelivered after each failure must
// wait longer every time, for every dispatch path that NAKs (the create
// start and the lifecycle dispatch alike).
func TestHandleTriggerMessage_NakDelaysGrowAcrossDeliveries(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	idpOrgRef := "375900000000000002"
	deleted := map[string]any{
		"deleted_at":  time.Now().UTC().Format(time.RFC3339Nano),
		"idp_org_ref": idpOrgRef,
		"status":      "done",
	}
	live := map[string]any{"deleted_at": nil, "idp_org_ref": idpOrgRef, "status": "done"}

	cases := []struct {
		name string
		msg  func(t *testing.T) *mocks.JetstreamMsg
	}{
		{"create start", func(t *testing.T) *mocks.JetstreamMsg {
			t.Helper()
			return tenantEventMsg(t, events.OpCreate, tenantID, nil, pendingRow(tenantID))
		}},
		{"lifecycle dispatch", func(t *testing.T) *mocks.JetstreamMsg {
			t.Helper()
			return tenantEventMsg(t, events.OpDelete, tenantID, live, deleted)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := testCfg()
			var delays []time.Duration
			for delivery := uint64(1); delivery < uint64(cfg.MaxRedeliver); delivery++ {
				msg := tc.msg(t)
				msg.Deliveries = delivery
				mockClient := new(mocks.MockTemporalClient)
				mockClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
					Return(new(temporalmocks.WorkflowRun), errors.New("temporal unreachable")).Once()

				handleTriggerMessage(t.Context(), msg, mockClient, cfg)

				got := msg.NakDelays()
				require.Len(t, got, 1, "delivery %d", delivery)
				delays = append(delays, got[0])
			}
			require.Len(t, delays, cfg.MaxRedeliver-1)
			for i := 1; i < len(delays); i++ {
				assert.Greater(t, delays[i], delays[i-1], "delay after delivery %d must exceed the one before", i+1)
			}
			assert.NotContains(t, delays, time.Duration(0), "a plain NAK is redelivered immediately")
		})
	}
}

// A delivery count that cannot be read must still NAK with a delay, the
// first one of the schedule, and say why at Warn: a plain NAK would be
// redelivered at once, and without the log line the fallback is invisible.
func TestHandleTriggerMessage_NaksWithTheFirstDelayWhenTheDeliveryCountIsUnreadable(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	msg := tenantEventMsg(t, events.OpCreate, tenantID, nil, pendingRow(tenantID))
	msg.Deliveries = 4 // would select the 30 s delay if it were readable
	msg.MetadataErr = errors.New("message is not a jetstream message")

	var logs bytes.Buffer
	logger := log.ForContext(t.Context()).Output(&logs).Level(log.DebugLevel)
	ctx := log.Context(t.Context(), logger)

	mockClient := new(mocks.MockTemporalClient)
	mockClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(new(temporalmocks.WorkflowRun), errors.New("connection refused")).Once()

	handleTriggerMessage(ctx, msg, mockClient, testCfg())

	assert.False(t, msg.IsAcked())
	assert.Equal(t, []time.Duration{redeliverBackoff[0]}, msg.NakDelays())
	assert.Contains(t, logs.String(), "could not read delivery count of tenant event")
	assert.Contains(t, logs.String(), `"level":"warn"`)
	assert.NotContains(t, logs.String(), "exhausted its redeliveries")
}

// A non-positive MaxDeliver is not a cap: JetStream reads it as unlimited
// redelivery. nak must then keep backing off by the delivery count and
// never log the drop: every delivery count is >= 0, so a missing guard
// would report each NAK as the last one and pin the delay to the first
// schedule entry.
func TestNak_NonPositiveMaxDeliverBacksOffWithoutExhaustionLog(t *testing.T) {
	t.Parallel()

	for _, maxDeliver := range []int{0, -1} {
		t.Run(fmt.Sprintf("maxDeliver %d", maxDeliver), func(t *testing.T) {
			t.Parallel()

			msg := newMsg(t, nil)
			msg.Deliveries = 3

			var logs bytes.Buffer
			logger := log.ForContext(t.Context()).Output(&logs).Level(log.DebugLevel)

			nak(logger, msg, maxDeliver)

			require.Equal(t, nakDelay(3), redeliverBackoff[2], "fixture: the third delivery selects the third delay")
			assert.Equal(t, []time.Duration{redeliverBackoff[2]}, msg.NakDelays())
			assert.NotContains(t, logs.String(), "exhausted its redeliveries")
		})
	}
}

// FuzzNakDelay guards the index nakDelay derives from the server-supplied
// delivery count: whatever NumDelivered a message carries, the delay must
// come from the schedule, and a later delivery must never wait less than
// an earlier one. A delay that shrinks is the bug class of the plain NAK
// this schedule replaced: a redelivery fired too early spends a delivery
// and brings a dropped create event closer. The count is not bounded by
// MaxDeliver in general: nak() treats a non-positive MaxDeliver as
// unlimited redelivery, so every uint64 is a legal input.
//
// Run continuously with:
//
//	go test ./events/tenants/ -run '^$' -fuzz FuzzNakDelay
//
// Under a plain `go test` the seed corpus below runs as ordinary regression.
func FuzzNakDelay(f *testing.F) {
	for _, n := range []uint64{
		// real: no count, every position of the schedule, the default cap
		0, 1, 2, 3, 4, 5, 6, 10,
		// hostile: the 32-bit int boundary, the int64 sign boundary, the top
		1<<31 - 1, 1 << 31, 1<<32 - 1, 1 << 32, 1<<63 - 2, 1<<63 - 1, math.MaxUint64 - 1, math.MaxUint64,
	} {
		f.Add(n)
	}

	last := redeliverBackoff[len(redeliverBackoff)-1]
	f.Fuzz(func(t *testing.T, n uint64) {
		got := nakDelay(n)

		if !slices.Contains(redeliverBackoff, got) {
			t.Fatalf("nakDelay(%d) = %s is not a schedule entry", n, got)
		}
		switch {
		case n <= 1:
			if got != redeliverBackoff[0] {
				t.Fatalf("nakDelay(%d) = %s, want the first delay %s", n, got, redeliverBackoff[0])
			}
		case n >= uint64(len(redeliverBackoff)):
			if got != last {
				t.Fatalf("nakDelay(%d) = %s, want the clamped last delay %s", n, got, last)
			}
		default:
			if want := redeliverBackoff[n-1]; got != want {
				t.Fatalf("nakDelay(%d) = %s, want %s", n, got, want)
			}
		}
		if n < math.MaxUint64 {
			if next := nakDelay(n + 1); next < got {
				t.Fatalf("delay shrinks: nakDelay(%d) = %s but nakDelay(%d) = %s", n, got, n+1, next)
			}
		}
	})
}
