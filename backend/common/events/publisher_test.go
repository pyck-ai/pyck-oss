package events_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/log"
)

func TestIsDuplicateMsgIDInProcess(t *testing.T) {
	t.Parallel()

	inProcess := &jetstream.APIError{Code: 409, ErrorCode: 10158, Description: "duplicate message id is in process"}
	other := &jetstream.APIError{Code: 400, ErrorCode: 10071, Description: "wrong last sequence"}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "bare api error", err: inProcess, want: true},
		{name: "wrapped like the publisher", err: fmt.Errorf("nats: %w", inProcess), want: true},
		{name: "wrapped twice", err: fmt.Errorf("outer: %w", fmt.Errorf("nats: %w", inProcess)), want: true},
		{name: "other api error code", err: other, want: false},
		{name: "wrapped other api error code", err: fmt.Errorf("nats: %w", other), want: false},
		{name: "plain error", err: errors.New("nats unavailable"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, events.IsDuplicateMsgIDInProcess(tt.err))
		})
	}
}

// failingJetStream embeds the interface so only PublishMsg needs overriding;
// any other method panics on the nil embedded value.
type failingJetStream struct {
	jetstream.JetStream

	err error
}

func (f failingJetStream) PublishMsg(context.Context, *nats.Msg, ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	return nil, f.err
}

// publishLogLevels sends a custom event through a publisher whose JetStream
// returns err and returns the levels of the log lines it wrote.
func publishLogLevels(t *testing.T, err error) []string {
	t.Helper()

	var buf bytes.Buffer

	ctx := log.Context(t.Context(), zerolog.New(&buf).Level(zerolog.DebugLevel))
	pub := events.NewEventPublisher(failingJetStream{err: err}, nil, "pyck")

	got := pub.SendCustomEvent(ctx, &events.CustomEventMessage{Type: "test"})
	require.Equal(t, err, got, "publish must return the error unchanged")

	var levels []string

	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}

		var entry struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}

		require.NoError(t, json.Unmarshal(line, &entry))
		assert.Equal(t, "publish nats message", entry.Message)

		levels = append(levels, entry.Level)
	}

	return levels
}

func TestPublish_LogsDuplicateInProcessAtDebug(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("nats: %w", &jetstream.APIError{Code: 409, ErrorCode: 10158, Description: "duplicate message id is in process"})

	assert.Equal(t, []string{"debug"}, publishLogLevels(t, err))
}

func TestPublish_LogsOtherAPIErrorAtError(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("nats: %w", &jetstream.APIError{Code: 400, ErrorCode: 10071, Description: "wrong last sequence"})

	assert.Equal(t, []string{"error"}, publishLogLevels(t, err))
}
