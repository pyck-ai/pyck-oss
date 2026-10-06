package services_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events"
)

func publishedStateChange() *events.TemporalWorkflowStateChangeMessage {
	return &events.TemporalWorkflowStateChangeMessage{
		Namespace:        "default",
		TaskQueue:        "queue",
		WorkflowID:       "wf-1",
		WorkflowTypeName: "Type",
		RunID:            "run-1",
		Status:           "Running",
	}
}

// TestSendTemporalWorkflowEventDeduplicates lives here because the workflow
// module already depends on the embedded NATS server.
func TestSendTemporalWorkflowEventDeduplicates(t *testing.T) {
	t.Parallel()

	srv, err := server.NewServer(&server.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
	})
	require.NoError(t, err)

	go srv.Start()

	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded NATS did not start")
	t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	ctx := context.Background()
	const streamName = "pyck"

	// Build the stream the way the services do, so a change to the
	// production stream config that broke dedup fails this test.
	js, err := events.CreateOrUpdateJetstream(ctx, nc, streamName, 1)
	require.NoError(t, err)

	stream, err := js.Stream(ctx, streamName)
	require.NoError(t, err)
	assert.Equal(t, events.StreamDuplicateWindow, stream.CachedInfo().Config.Duplicates)

	publisher := events.NewEventPublisher(js, nc, streamName)

	// Two pods publishing the same change store one message.
	require.NoError(t, publisher.SendTemporalWorkflowEvent(ctx, publishedStateChange()))
	require.NoError(t, publisher.SendTemporalWorkflowEvent(ctx, publishedStateChange()))

	info, err := stream.Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), info.State.Msgs)

	msg, err := stream.GetMsg(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t,
		events.StateChangeMsgIDPrefix+publishedStateChange().EventID().String(),
		msg.Header.Get(jetstream.MsgIDHeader),
	)

	// A different status is a different change.
	next := publishedStateChange()
	next.Status = "Completed"
	require.NoError(t, publisher.SendTemporalWorkflowEvent(ctx, next))

	info, err = stream.Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), info.State.Msgs)
}
