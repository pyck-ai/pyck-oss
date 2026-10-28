package events_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/events/topic"
	"github.com/pyck-ai/pyck/backend/common/test/natstest"
)

//nolint:tparallel,paralleltest // the aggregate count after the subtests needs both done
func TestSendTemporalWorkflowEvent(t *testing.T) {
	t.Parallel()

	nc, js := newJetStream(t)
	ctx := t.Context()

	// Build the streams the way the services do, so a change to the
	// production stream config that broke dedup fails this test.
	_, err := events.SetupJetStream(ctx, nc, testPrefix, 1, testLimits)
	require.NoError(t, err)

	tenantID := uuid.New()
	require.NoError(t, events.NewStreamTopology(js, testPrefix, 1, natstest.SerialLock(), testLimits).EnsureTenant(ctx, tenantID))

	publisher := events.NewEventPublisher(js, nc, testPrefix)

	for name, tc := range map[string]struct {
		namespace string
		stream    string
	}{
		"a tenant namespace goes to the tenant's stream": {
			namespace: tenantID.String(),
			stream:    topic.TenantStreamName(testPrefix, tenantID),
		},
		"a namespace of no tenant goes to the system stream": {
			namespace: "default",
			stream:    topic.SystemStreamName(testPrefix),
		},
	} {
		t.Run(name, func(t *testing.T) {
			change := &events.TemporalWorkflowStateChangeMessage{
				Namespace:        tc.namespace,
				TaskQueue:        "queue",
				WorkflowID:       "wf-1",
				WorkflowTypeName: "Type",
				RunID:            "run-1",
				Status:           "Running",
			}

			// Two pods publishing the same change store one message.
			require.NoError(t, publisher.SendTemporalWorkflowEvent(ctx, change))
			require.NoError(t, publisher.SendTemporalWorkflowEvent(ctx, change))

			stream, err := js.Stream(ctx, tc.stream)
			require.NoError(t, err)
			assert.Equal(t, events.StreamDuplicateWindow, stream.CachedInfo().Config.Duplicates)

			info, err := stream.Info(ctx)
			require.NoError(t, err)
			assert.Equal(t, uint64(1), info.State.Msgs)

			msg, err := stream.GetMsg(ctx, 1)
			require.NoError(t, err)
			assert.Equal(t, events.StateChangeMsgIDPrefix+change.EventID().String(), msg.Header.Get(jetstream.MsgIDHeader))

			// A different status is a different change.
			next := *change
			next.Status = "Completed"
			require.NoError(t, publisher.SendTemporalWorkflowEvent(ctx, &next))

			info, err = stream.Info(ctx)
			require.NoError(t, err)
			assert.Equal(t, uint64(2), info.State.Msgs)
		})
	}

	// Both streams reach the aggregate the signal router reads.
	awaitMsgs(t, ctx, js, testPrefix, 4)
}

func TestSendCustomEventGoesToTheSystemStream(t *testing.T) {
	t.Parallel()

	nc, js := newJetStream(t)
	ctx := t.Context()

	_, err := events.SetupJetStream(ctx, nc, testPrefix, 1, testLimits)
	require.NoError(t, err)

	publisher := events.NewEventPublisher(js, nc, testPrefix)
	require.NoError(t, publisher.SendCustomEvent(ctx, &events.CustomEventMessage{Type: "user", Operation: "login"}))

	awaitMsgs(t, ctx, js, topic.SystemStreamName(testPrefix), 1)
}
