package resolvers

import (
	"errors"
	"fmt"

	"github.com/pyck-ai/pyck/backend/common/events/topic"

	"github.com/pyck-ai/pyck/backend/management/model"
)

// ErrBlankEventToken is returned when a custom event's type or operation
// leaves nothing to put into its subject segment.
var ErrBlankEventToken = errors.New("custom event type and operation must not be blank")

// validateCustomEvent refuses a custom event whose type or operation cannot
// become a subject segment of its own.
func validateCustomEvent(input model.SendCustomEventInput) error {
	if isBlankToken(input.Type) {
		return fmt.Errorf("%w: type %q", ErrBlankEventToken, input.Type)
	}
	if isBlankToken(input.Operation) {
		return fmt.Errorf("%w: operation %q", ErrBlankEventToken, input.Operation)
	}
	return nil
}

// isBlankToken reports whether s has no segment of its own: "" becomes the
// wildcard "*", which no subscriber of a concrete type receives, and
// whitespace-only input becomes an empty segment, a subject NATS refuses, so
// the outbox would retry the event until it is dead-lettered.
func isBlankToken(s string) bool {
	token := topic.NormalizeToken(s)
	return token == "" || token == "*"
}
