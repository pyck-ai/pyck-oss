package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var sendCustomEvent = resolver.ParseTemplate(`mutation {
	sendCustomEvent(input: {
		type: "TestEvent",
		operation: "logout",
		payload: {
			id: "{{.ID}}",
			data: { name: "custom" }
		}
	}) {
		success
	}
}`)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type sendCustomEventData struct {
	SendCustomEvent struct {
		Success bool
	}
}

// =============================================================================
// SEND CUSTOM EVENT TESTS
// =============================================================================

func TestSendCustomEvent(t *testing.T) {
	t.Parallel()

	t.Run("sends custom event successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		eventID := uuid.New()
		data := execOK[sendCustomEventData](te, ctx, sendCustomEvent, map[string]any{
			"ID": eventID.String(),
		})

		assert.True(t, data.SendCustomEvent.Success)

		// Custom event should be inserted into the outbox for the handler to publish
		te.assertEvents(ctx, Event{"testevent", eventID, "logout"})
	})
}
