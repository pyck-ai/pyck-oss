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

// makeEventMsg marshals payload into a nats.Msg with the given reply topic.
func makeEventMsg(t *testing.T, payload any, reply string) *nats.Msg {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return &nats.Msg{
		Subject: events.MutationEventTopic{StreamName: "pyck"}.String(),
		Reply:   reply,
		Data:    data,
	}
}

// validMsg returns a correctly populated mutation event message for the given operation.
func validMsg(t *testing.T, operation, reply string) *nats.Msg {
	t.Helper()
	return makeEventMsg(t, events.MutationEventMessage{
		Type:      "inventoryinbound",
		TenantID:  testTenantID,
		Operation: operation,
	}, reply)
}

// TestHandleMutationEvent_ValidationErrors verifies that HandleMutationEvent
// (the shared core used by both sync and async paths) rejects malformed messages
// before reaching the database.
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
			}, ""),
			wantErr: services.ErrInvalidEventMessage,
		},
		{
			name: "zero tenant ID",
			msg: makeEventMsg(t, events.MutationEventMessage{
				Type:      "inventoryinbound",
				Operation: "create",
				// TenantID intentionally left as uuid.Nil
			}, ""),
			wantErr: services.ErrInvalidEventMessage,
		},
		{
			name:    "unknown operation",
			msg:     validMsg(t, "publish", ""),
			wantErr: services.ErrUnknownOperation,
		},
		{
			name:    "unknown operation - empty string",
			msg:     validMsg(t, "", ""),
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

// TestHandleMutationEventWithReply_RejectsEmptyReplyTopic verifies that the
// sync (request/reply) wrapper rejects messages that carry no reply address,
// which would prevent the outbox handler from receiving the workflow IDs.
func TestHandleMutationEventWithReply_RejectsEmptyReplyTopic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	router := &services.SignalRouter{}

	msg := validMsg(t, "create", "") // no reply topic

	_, err := router.HandleMutationEventWithReply(ctx, msg)
	if err == nil {
		t.Fatal("expected ErrInvalidEventMessage for missing reply topic, got nil")
	}
	if !errors.Is(err, services.ErrInvalidEventMessage) {
		t.Errorf("want ErrInvalidEventMessage, got: %v", err)
	}
}

// TestHandleMutationEventWithReply_DelegatesToHandleMutationEvent verifies that
// the sync wrapper's own validation fires before reaching HandleMutationEvent,
// and that HandleMutationEvent's errors are also surfaced through the wrapper.
// This covers the delegation chain without needing a live DB.
func TestHandleMutationEventWithReply_DelegatesToHandleMutationEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	router := &services.SignalRouter{}

	tests := []struct {
		name    string
		msg     *nats.Msg
		wantErr error
	}{
		{
			// Reply is missing → caught by the wrapper itself, before HandleMutationEvent.
			name:    "missing reply topic",
			msg:     validMsg(t, "create", ""),
			wantErr: services.ErrInvalidEventMessage,
		},
		{
			// Reply is present, but the payload is invalid → caught by HandleMutationEvent.
			name: "invalid payload with reply",
			msg: &nats.Msg{
				Data:  []byte("not-json"),
				Reply: "inbox.reply",
			},
			wantErr: services.ErrInvalidEventMessage,
		},
		{
			// Reply is present, operation is unknown → caught by HandleMutationEvent.
			name:    "unknown operation with reply",
			msg:     validMsg(t, "publish", "inbox.reply"),
			wantErr: services.ErrUnknownOperation,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := router.HandleMutationEventWithReply(ctx, tc.msg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("want errors.Is(%v), got: %v", tc.wantErr, err)
			}
		})
	}
}

// TestAsyncVsSync_ReplyTopicRequirement explicitly documents the behavioral
// difference between the async (fire-and-forget) and sync (request/reply) paths:
// the async path does not require a reply topic; the sync path does.
func TestAsyncVsSync_ReplyTopicRequirement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	router := &services.SignalRouter{}

	// Message with no reply topic but an unknown operation: it will fail at the
	// operation-validation step (ErrUnknownOperation) rather than at a reply-topic
	// check, proving the async path never inspects msg.Reply.
	asyncMsg := makeEventMsg(t, events.MutationEventMessage{
		Type:      "inventoryinbound",
		TenantID:  testTenantID,
		Operation: "unknown-op",
	}, "")

	t.Run("async path accepts missing reply topic", func(t *testing.T) {
		t.Parallel()
		_, err := router.HandleMutationEvent(ctx, asyncMsg)
		// Must fail with ErrUnknownOperation (reached operation validation),
		// not ErrInvalidEventMessage (which would indicate reply was checked).
		if errors.Is(err, services.ErrInvalidEventMessage) {
			t.Error("async path must not reject messages without a reply topic")
		}
		if !errors.Is(err, services.ErrUnknownOperation) {
			t.Errorf("expected ErrUnknownOperation after passing reply check, got: %v", err)
		}
	})

	t.Run("sync path rejects missing reply topic", func(t *testing.T) {
		t.Parallel()
		_, err := router.HandleMutationEventWithReply(ctx, asyncMsg)
		if !errors.Is(err, services.ErrInvalidEventMessage) {
			t.Errorf("sync path must reject missing reply topic, got: %v", err)
		}
	})
}
