package services_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events"

	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
	"github.com/pyck-ai/pyck/backend/workflow/services"
)

// subscribeTo registers a live subscription on topic.
func (e *fanoutEnv) subscribeTo(topic events.Topic, typ entworkflowsignal.TemporalSignalType, signal string) {
	e.t.Helper()

	e.db.WorkflowSignal.Create().
		SetTenantID(e.tenantID).
		SetWorkflowID(e.wf.ID).
		SetNatsTopic(topic.String()).
		SetTemporalSignalType(typ).
		SetTemporalSignal(signal).
		SetWorkerID("worker").
		SetExpiresAt(time.Now().UTC().Add(time.Hour)).
		SaveX(e.ctx)
}

// customType is the type of the custom events these tests publish.
const customType = "tour-creation"

// publishCustom routes one custom event of e's tenant, as sendCustomEvent
// publishes it, on the subject of subjectTenant.
func (e *fanoutEnv) publishCustom(subjectTenant uuid.UUID, operation string) error {
	e.t.Helper()

	entityID := uuid.New()
	data, err := json.Marshal(events.MutationEventMessage{
		Service:   "management",
		Type:      customType,
		Schema:    customType,
		Operation: operation,
		ID:        entityID,
		EventID:   uuid.New(),
		TenantID:  e.tenantID,
		DataAfter: map[string]any{"id": entityID.String()},
	})
	require.NoError(e.t, err)

	_, err = e.router.HandleMutationEvent(e.ctx, &nats.Msg{
		Subject: events.TenantCustomEventTopic{
			TenantID: subjectTenant, EventType: customType, EntityID: entityID, OperationName: operation,
		}.String(),
		Data: data,
	})

	return err
}

func TestRouter_CustomEvent_StartsAndSignalsCustomSubscriptions(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribeTo(events.TenantCustomEventTopic{TenantID: e.tenantID, EventType: customType, OperationName: "create"}, sigStart, "")
	e.subscribeTo(events.TenantCustomEventTopic{TenantID: e.tenantID, EventType: customType}, sigInter, "TourEvent")

	require.NoError(t, e.publishCustom(e.tenantID, "create"))

	assert.Len(t, e.temporal.starts, 1)
	assert.Equal(t, []string{"TourEvent"}, e.temporal.signals)
}

// A custom event shares no subject with CRUD events, so a CRUD subscription
// of the same name never fires on it.
func TestRouter_CustomEvent_DoesNotReachCrudSubscriptions(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribeTo(events.MutationEventTopic{TenantID: e.tenantID, ServiceName: "management", SchemaName: customType, OperationName: "create"}, sigStart, "")
	e.subscribeTo(events.MutationEventTopic{TenantID: e.tenantID}, sigInter, "AnyCrud")

	require.NoError(t, e.publishCustom(e.tenantID, "create"))

	assert.Empty(t, e.temporal.starts)
	assert.Empty(t, e.temporal.signals)
}

// An operation other than create, update and delete starts the subscriptions
// on its subject with DefaultUpdatePolicy and signals the intermediate ones.
func TestRouter_CustomEvent_RoutesAnyOperation(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribeTo(events.TenantCustomEventTopic{TenantID: e.tenantID, EventType: customType, OperationName: "ping"}, sigStart, "")
	e.subscribeTo(events.TenantCustomEventTopic{TenantID: e.tenantID, EventType: customType}, sigInter, "TourEvent")

	require.NoError(t, e.publishCustom(e.tenantID, "ping"))

	require.Len(t, e.temporal.startOptions, 1)
	assert.Equal(t, services.DefaultUpdatePolicy.WorkflowIDReusePolicy, e.temporal.startOptions[0].WorkflowIDReusePolicy)
	assert.Equal(t, []string{"TourEvent"}, e.temporal.signals)
}

// The subscriptions are bound to their tenant: an event on another tenant's
// subject starts and signals nothing, whatever tenant its payload names.
func TestRouter_CustomEvent_OfAnotherTenantDoesNothing(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribeTo(events.TenantCustomEventTopic{TenantID: e.tenantID, EventType: customType, OperationName: "create"}, sigStart, "")
	e.subscribeTo(events.TenantCustomEventTopic{TenantID: e.tenantID}, sigInter, "AnyCustom")

	require.NoError(t, e.publishCustom(uuid.New(), "create"))

	assert.Empty(t, e.temporal.starts)
	assert.Empty(t, e.temporal.signals)
}

// publishCustom publishes a custom event with m's operation, as
// sendCustomEvent does.
func (e *statusEnv) publishCustom(m mutation) {
	e.t.Helper()

	data, err := json.Marshal(events.MutationEventMessage{
		Service: "management", Type: customType, Schema: customType, Operation: m.operation,
		ID: m.entity, EventID: m.eventID, TenantID: e.tenantID,
		WfSearchAttributes: map[string]string{"pyck_transaction_id": m.txID.String()},
	})
	require.NoError(e.t, err)

	subject := events.TenantCustomEventTopic{
		StreamName: consumerStream, TenantID: e.tenantID, EventType: customType, EntityID: m.entity, OperationName: m.operation,
	}.String()
	_, err = e.js.Publish(e.t.Context(), subject, data)
	require.NoError(e.t, err)
}

// A custom event gets a routing status under its transaction, which is how
// sendCustomEvent's caller finds the workflows it started.
func TestRoutingStatus_CustomEventIsRecorded(t *testing.T) {
	t.Parallel()

	e := newStatusEnv(t, nil)
	e.subscribeTo(events.TenantCustomEventTopic{TenantID: e.tenantID, EventType: customType, OperationName: "create"}, sigStart, "")

	m := newMutation()
	e.publishCustom(m)

	got := e.status(m)

	assert.Equal(t, services.RoutingOutcomeDone, got.Outcome)
	assert.Equal(t, []services.RoutingTargetKind{services.RoutingTargetStarted}, kinds(got.Targets))
}

// A custom event carries the operation its sender chose, so any operation is
// routed, not only create, update and delete.
func TestRoutingStatus_CustomEventOfAnyOperation(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		subscribed string
		want       []services.RoutingTargetKind
	}{
		"subscribed":   {subscribed: "approve", want: []services.RoutingTargetKind{services.RoutingTargetStarted}},
		"unsubscribed": {subscribed: "reject", want: []services.RoutingTargetKind{}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			e := newStatusEnv(t, nil)
			e.subscribeTo(events.TenantCustomEventTopic{TenantID: e.tenantID, EventType: customType, OperationName: tc.subscribed}, sigStart, "")

			m := newMutation()
			m.operation = "approve"
			e.publishCustom(m)

			got := e.status(m)

			assert.Equal(t, services.RoutingOutcomeDone, got.Outcome)
			assert.Equal(t, tc.want, kinds(got.Targets))
		})
	}
}
