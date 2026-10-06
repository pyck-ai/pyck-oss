package resolvers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/common/txid"

	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
)

// signalEntityID is the concrete entity the legacy signal below subscribes to.
var signalEntityID = uuid.MustParse("123e4567-e89b-12d3-a456-426614174000")

// legacyReplyTopic returns a signal topic in the legacy request/reply form
// that external registrants (e.g. workflow SDK users) stored before mutation
// events moved to fire-and-forget publishing.
func legacyReplyTopic(tenantID uuid.UUID) string {
	return fmt.Sprintf("request.reply.pyck.%s.crud.workflow.workflowsignal.%s.create",
		tenantID, signalEntityID)
}

// TestSignalTopicNormalization_LegacyReplyTopicTriggersOnFireAndForget covers
// the full chain that keeps legacy registrations working once mutation events
// are only published fire-and-forget:
//
//  1. registerWorkflow accepts a request.reply.* signal topic but stores the
//     normalized fire-and-forget form (topic matching is type-strict, so the
//     stored prefix form would never match a fire-and-forget event).
//  2. A mutation event arriving on the fire-and-forget subject triggers the
//     workflow registered with the legacy topic.
//  3. The started execution carries the event's pyck_transaction_id as a
//     typed search attribute — the handle clients use to look up what a
//     mutation started.
func TestSignalTopicNormalization_LegacyReplyTopicTriggersOnFireAndForget(t *testing.T) {
	t.Parallel()

	te := setupWithMockWorkflow(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	execOK[registerWorkflowData](te, ctx, registerWorkflow, map[string]any{
		"Name":       "wf_legacy_topic_normalization",
		"TaskQueue":  "test-queue",
		"DataTypeID": itemDataTypeID,
		"DataName":   "wf_legacy_topic_normalization",
		"DataWeight": 0,
		"Signals": []SignalInput{
			{NATSTopic: legacyReplyTopic(tenantA), TemporalSignal: "", TemporalSignalType: "start", FilterRule: "true"},
		},
	})

	// 1. The stored topic must be the normalized fire-and-forget form.
	signals, err := te.Ent.WorkflowSignal.Query().
		Where(entworkflowsignal.TenantIDEQ(tenantA), entworkflowsignal.DeletedAtIsNil()).
		Limit(2).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, signals, 1)
	wantTopic := fmt.Sprintf("pyck.%s.crud.workflow.workflowsignal.%s.create", tenantA, signalEntityID)
	assert.Equal(t, wantTopic, signals[0].NatsTopic,
		"legacy request.reply.* registrations must be stored in fire-and-forget form")

	// 2.+3. A fire-and-forget mutation event must trigger the workflow and
	// stamp the txid search attribute at StartWorkflow.
	var capturedOpts temporalclient.StartWorkflowOptions
	mockRun := mocks.NewMockWorkflowRun("wf-id-1", "run-id-1", nil, nil)
	te.MockTemporalClient.
		On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			opts, ok := args.Get(1).(temporalclient.StartWorkflowOptions)
			require.True(t, ok, "ExecuteWorkflow must receive StartWorkflowOptions as its second argument")
			capturedOpts = opts
		}).
		Return(mockRun, nil)

	transactionID := txid.New()
	event := events.MutationEventMessage{
		Service:   "workflow",
		Type:      "workflowworkflowsignal",
		Schema:    "workflowsignal",
		Operation: "create",
		ID:        signalEntityID,
		TenantID:  tenantA,
		DataAfter: map[string]any{},
		WfSearchAttributes: map[string]string{
			"pyck_tenant_id":      tenantA.String(),
			"pyck_transaction_id": transactionID.String(),
		},
	}
	payload, err := json.Marshal(event)
	require.NoError(t, err)

	subject := events.MutationEventTopic{
		StreamName:    "pyck",
		TenantID:      tenantA,
		ServiceName:   "workflow",
		SchemaName:    "workflowsignal",
		EntityID:      signalEntityID,
		OperationName: "create",
	}.String()

	started, err := te.SignalRouter.HandleMutationEvent(context.Background(), &nats.Msg{
		Subject: subject,
		Data:    payload,
	})
	require.NoError(t, err)
	require.Len(t, started, 1, "the legacy-registered signal must trigger on the fire-and-forget event")
	assert.Equal(t, "wf_legacy_topic_normalization", started[0].Type)
	assert.Equal(t, "wf-id-1", started[0].ID)
	assert.Equal(t, "run-id-1", started[0].RunID)

	// The typed search attributes handed to Temporal must carry the txid.
	got := ""
	for key, value := range capturedOpts.TypedSearchAttributes.GetUntypedValues() {
		if key.GetName() == "pyck_transaction_id" {
			got, _ = value.(string)
		}
	}
	assert.Equal(t, transactionID.String(), got,
		"StartWorkflow must stamp pyck_transaction_id from the event's search attributes")
}
