package resolvers

import (
	"errors"
	"fmt"

	commondatatype "github.com/pyck-ai/pyck/backend/common/datatype"
	"github.com/pyck-ai/pyck/backend/common/events/topic"

	"github.com/pyck-ai/pyck/backend/management/model"
)

var (
	// ErrReservedEventType is returned when a custom event names the type of a
	// management entity.
	ErrReservedEventType = errors.New("custom event type is reserved for a management entity")

	// ErrBlankEventToken is returned when a custom event's type or operation
	// leaves nothing to put into its subject segment.
	ErrBlankEventToken = errors.New("custom event type and operation must not be blank")
)

// reservedEventTypes are the management entity types. A custom event is
// re-published on pyck.<tenant>.crud.management.<type>.<id>.<op>, the subject
// an entity's own CRUD events use, and consumers such as every service's
// datatype cache subscribe to those for all tenants and trust the payload's
// id. A custom event under one of these types would be indistinguishable
// from a real change of another tenant's row.
var reservedEventTypes = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, t := range commondatatype.ServiceEntities()[topic.ManagementService] {
		m[topic.NormalizeToken(t)] = struct{}{}
	}
	return m
}()

// validateCustomEvent refuses a custom event whose type or operation cannot
// become a subject segment of its own, or whose type is a management entity's.
func validateCustomEvent(input model.SendCustomEventInput) error {
	if isBlankToken(input.Type) {
		return fmt.Errorf("%w: type %q", ErrBlankEventToken, input.Type)
	}
	if isBlankToken(input.Operation) {
		return fmt.Errorf("%w: operation %q", ErrBlankEventToken, input.Operation)
	}
	if isReservedEventType(input.Type) {
		return fmt.Errorf("%w: %q", ErrReservedEventType, input.Type)
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

// isReservedEventType reports whether typ lands on the subject segment of a
// management entity, whatever its spelling.
func isReservedEventType(typ string) bool {
	_, ok := reservedEventTypes[topic.NormalizeToken(typ)]
	return ok
}
