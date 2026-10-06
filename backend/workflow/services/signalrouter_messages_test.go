package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/pyck-ai/pyck/backend/common/events"

	"github.com/pyck-ai/pyck/backend/workflow/services"
)

var testTenantID = uuid.MustParse("00000000-0000-0000-0000-000000000001") //nolint:gochecknoglobals

// makeEventMsg marshals payload into a nats.Msg on the mutation-event subject.
func makeEventMsg(t *testing.T, payload any) *nats.Msg {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return &nats.Msg{
		Subject: events.MutationEventTopic{StreamName: "pyck"}.String(),
		Data:    data,
	}
}

// validMsg returns a correctly populated mutation event message for the given operation.
func validMsg(t *testing.T, operation string) *nats.Msg {
	t.Helper()
	return makeEventMsg(t, events.MutationEventMessage{
		Type:      "inventoryinbound",
		TenantID:  testTenantID,
		Operation: operation,
	})
}

// TestHandleMutationEvent_ValidationErrors verifies that HandleMutationEvent
// rejects malformed messages before reaching the database.
func TestHandleMutationEvent_ValidationErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	router := &services.SignalRouter{} // DB not reached for these error paths

	tests := []struct {
		name    string
		msg     *nats.Msg
		wantErr error
	}{
		{
			name:    "invalid JSON",
			msg:     &nats.Msg{Data: []byte("not-valid-json")},
			wantErr: services.ErrInvalidEventMessage,
		},
		{
			name: "empty event type",
			msg: makeEventMsg(t, events.MutationEventMessage{
				TenantID:  testTenantID,
				Operation: "create",
				// Type intentionally left empty
			}),
			wantErr: services.ErrInvalidEventMessage,
		},
		{
			name: "zero tenant ID",
			msg: makeEventMsg(t, events.MutationEventMessage{
				Type:      "inventoryinbound",
				Operation: "create",
				// TenantID intentionally left as uuid.Nil
			}),
			wantErr: services.ErrInvalidEventMessage,
		},
		{
			name:    "unknown operation",
			msg:     validMsg(t, "publish"),
			wantErr: services.ErrUnknownOperation,
		},
		{
			name:    "unknown operation - empty string",
			msg:     validMsg(t, ""),
			wantErr: services.ErrUnknownOperation,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := router.HandleMutationEvent(ctx, tc.msg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("want errors.Is(%v), got: %v", tc.wantErr, err)
			}
		})
	}
}

// TestHandleMutationEvent_NoReplyTopicRequired documents that the
// fire-and-forget path never inspects msg.Reply: a message without a reply
// topic proceeds to operation validation instead of being rejected.
func TestHandleMutationEvent_NoReplyTopicRequired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	router := &services.SignalRouter{}

	// Message with no reply topic but an unknown operation: it will fail at the
	// operation-validation step (ErrUnknownOperation) rather than at a reply-topic
	// check, proving the handler never inspects msg.Reply.
	msg := makeEventMsg(t, events.MutationEventMessage{
		Type:      "inventoryinbound",
		TenantID:  testTenantID,
		Operation: "unknown-op",
	})

	_, err := router.HandleMutationEvent(ctx, msg)
	// Must fail with ErrUnknownOperation (reached operation validation),
	// not ErrInvalidEventMessage (which would indicate reply was checked).
	if errors.Is(err, services.ErrInvalidEventMessage) {
		t.Error("handler must not reject messages without a reply topic")
	}
	if !errors.Is(err, services.ErrUnknownOperation) {
		t.Errorf("expected ErrUnknownOperation after passing validation, got: %v", err)
	}
}
