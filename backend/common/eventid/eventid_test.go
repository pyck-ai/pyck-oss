package eventid_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"

	"github.com/pyck-ai/pyck/backend/common/eventid"
)

func TestPayloadRoundTrip(t *testing.T) {
	t.Parallel()

	id := uuid.Must(uuid.NewV7())

	p, err := eventid.Payload(id)
	require.NoError(t, err)

	got, ok := eventid.Parse(p)
	require.True(t, ok)
	assert.Equal(t, id, got)
}

func TestParseRejectsGarbage(t *testing.T) {
	t.Parallel()

	_, ok := eventid.Parse(nil)
	assert.False(t, ok, "nil payload")

	_, ok = eventid.Parse(&commonpb.Payload{Data: []byte(`"not-a-uuid"`)})
	assert.False(t, ok, "not a UUID")
}

func TestContext(t *testing.T) {
	t.Parallel()

	_, ok := eventid.FromContext(context.Background())
	assert.False(t, ok)

	id := uuid.Must(uuid.NewV7())
	got, ok := eventid.FromContext(eventid.WithEventID(context.Background(), id))
	require.True(t, ok)
	assert.Equal(t, id, got)
}

func invoke(t *testing.T, ctx context.Context, req any) {
	t.Helper()

	invoker := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }
	require.NoError(t, eventid.ClientInterceptor()(ctx, "/m", req, nil, nil, invoker))
}

func header(t *testing.T, h *commonpb.Header) (uuid.UUID, bool) {
	t.Helper()

	if h == nil {
		return uuid.Nil, false
	}

	return eventid.Parse(h.GetFields()[eventid.HeaderKey])
}

func TestClientInterceptorSetsHeader(t *testing.T) {
	t.Parallel()

	id := uuid.Must(uuid.NewV7())
	ctx := eventid.WithEventID(context.Background(), id)

	start := &workflowservice.StartWorkflowExecutionRequest{}
	signal := &workflowservice.SignalWorkflowExecutionRequest{}
	sws := &workflowservice.SignalWithStartWorkflowExecutionRequest{}

	invoke(t, ctx, start)
	invoke(t, ctx, signal)
	invoke(t, ctx, sws)

	for name, h := range map[string]*commonpb.Header{"start": start.GetHeader(), "signal": signal.GetHeader(), "signal-with-start": sws.GetHeader()} {
		got, ok := header(t, h)
		require.True(t, ok, name)
		assert.Equal(t, id, got, name)
	}
}

func TestClientInterceptorKeepsOtherHeaderFields(t *testing.T) {
	t.Parallel()

	other := &commonpb.Payload{Data: []byte("x")}
	req := &workflowservice.StartWorkflowExecutionRequest{
		Header: &commonpb.Header{Fields: map[string]*commonpb.Payload{"traceparent": other}},
	}

	invoke(t, eventid.WithEventID(context.Background(), uuid.Must(uuid.NewV7())), req)

	assert.Same(t, other, req.GetHeader().GetFields()["traceparent"])
	assert.Contains(t, req.GetHeader().GetFields(), eventid.HeaderKey)
}

func TestClientInterceptorNoOpWithoutID(t *testing.T) {
	t.Parallel()

	req := &workflowservice.StartWorkflowExecutionRequest{}

	invoke(t, context.Background(), req)

	assert.Nil(t, req.GetHeader(), "no event ID on the context: request is untouched")
}

func TestClientInterceptorIgnoresOtherRequests(t *testing.T) {
	t.Parallel()

	req := &workflowservice.QueryWorkflowRequest{}

	invoke(t, eventid.WithEventID(context.Background(), uuid.Must(uuid.NewV7())), req)

	assert.Nil(t, req.GetQuery(), "other request types pass through")
}
