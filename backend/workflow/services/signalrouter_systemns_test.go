package services_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events"

	"github.com/pyck-ai/pyck/backend/workflow/services"
)

func TestSystemNamespaceStateChange(t *testing.T) {
	t.Parallel()

	const tenant = "0190f0f0-0000-7000-8000-000000000001"

	tests := []struct {
		name      string
		subject   string
		namespace string
		want      bool
	}{
		{"default namespace", "pyck.default.temporal.q.t.w.r.running", "default", true},
		{"temporal-system namespace", "pyck.temporal-system.temporal.q.t.w.r.completed", "temporal-system", true},
		{"tenant UUID", "pyck." + tenant + ".temporal.q.t.w.r.running", "", false},
		{"wildcard namespace", "pyck.*.temporal.q.t.w.r.running", "", false},
		{"not a temporal subject", "pyck.default.crud.inventory.item.x.create", "", false},
		{"too few tokens", "pyck.default.temporal.q.t.w.r", "", false},
		{"too many tokens", "pyck.default.temporal.q.t.w.r.running.extra", "", false},
		{"empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			namespace, ok := services.SystemNamespaceStateChange(tt.subject)
			assert.Equal(t, tt.want, ok)
			assert.Equal(t, tt.namespace, namespace)
		})
	}
}

func TestDispatchEvent_SystemNamespaceStateChangeIsSkipped(t *testing.T) {
	t.Parallel()

	for _, namespace := range []string{"default", "temporal-system"} {
		t.Run(namespace, func(t *testing.T) {
			t.Parallel()

			e := newFanoutEnv(t)
			e.subscribeStateChange("worker-a", sigStart, "")

			logs := &syncBuffer{}
			ctx := zerolog.New(logs).Level(zerolog.DebugLevel).WithContext(e.ctx)

			// The body is garbage on purpose: a skipped event is never read.
			err := e.router.DispatchEvent(ctx, &nats.Msg{
				Subject: "pyck." + namespace + ".temporal." + fanoutTaskQueue + ".other.other-1.run-1.running",
				Data:    []byte("not-json"),
			})

			require.NoError(t, err, "a system-namespace state change is handled, not failed")
			assert.Empty(t, e.temporal.starts, "no workflow is started for a system namespace")
			assert.Equal(t, 1, logs.count(`"level":"debug"`), "the skip is logged at debug")
			assert.Zero(t, logs.count(`"level":"warn"`), "and not as a warning")
			assert.Equal(t, 1, logs.count(`"namespace":"`+namespace+`"`), "the log names the namespace")
		})
	}
}

func TestDispatchEvent_TenantStateChangeStillRoutes(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribeStateChange("worker-a", sigStart, "")

	data, err := json.Marshal(events.TemporalWorkflowStateChangeMessage{
		Namespace: e.tenantID.String(), TaskQueue: fanoutTaskQueue,
		WorkflowTypeName: "Other", WorkflowID: "other_1", RunID: "run-1", Status: "COMPLETED",
	})
	require.NoError(t, err)

	err = e.router.DispatchEvent(context.Background(), &nats.Msg{
		Subject: events.TemporalWorkflowStateChangeTopic{
			StreamName: "pyck", Namespace: e.tenantID.String(), TaskQueue: fanoutTaskQueue,
			WorkflowTypeName: "Other", WorkflowID: "other_1", RunID: "run-1", Status: "COMPLETED",
		}.String(),
		Data: data,
	})

	require.NoError(t, err)
	assert.Len(t, e.temporal.starts, 1, "a tenant state change reaches the handler as before")
}

func TestDispatchEvent_MalformedSubjectStaysPermanent(t *testing.T) {
	t.Parallel()

	for _, subject := range []string{
		"pyck.default.crud.inventory.item.x.create",  // non-UUID tenant of a mutation event
		"pyck.tenant.temporal.q.t.w.r.running.extra", // too many tokens
		"not-a-known-subject",                        // no pattern
		"pyck.default.temporal.q.t.w.r",              // too few tokens
	} {
		t.Run(subject, func(t *testing.T) {
			t.Parallel()

			e := newFanoutEnv(t)

			logs := &syncBuffer{}
			ctx := zerolog.New(logs).WithContext(e.ctx)

			err := e.router.DispatchEvent(ctx, &nats.Msg{Subject: subject, Data: []byte(`{}`)})

			require.Error(t, err)
			assert.True(t, services.IsPermanent(err), "a subject that does not parse is a permanent failure")
		})
	}
}
