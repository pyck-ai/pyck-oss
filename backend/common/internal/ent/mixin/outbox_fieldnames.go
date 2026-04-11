// Package mixin provides internal constants and utilities for Ent mixins.
// This package is internal to avoid circular dependencies and ensure field name
// constants are only used by authorized common code (like event handlers).
package mixin

// Outbox database column names as defined by OutboxMixin.
// These constants represent the canonical field names that all services MUST use.
// Do not use these constants in service code - use the generated entityeventsoutbox.Field* constants instead.

// Database column names for the outbox table (as defined by OutboxMixin).
// These are snake_case database column names, not Go struct field names.
const (
	// ID is the primary key column.
	ID = "id"

	// CreatedAt is the timestamp when the entry was created.
	CreatedAt = "created_at"

	// PublishedAt is the timestamp when the event was published (NULL if not published).
	PublishedAt = "published_at"

	// UserID is the ID of the user who triggered the mutation.
	UserID = "user_id"

	// CorrelationID is the trace ID for correlating events across services.
	CorrelationID = "correlation_id"

	// Topic is the NATS topic to publish to.
	Topic = "topic"

	// Payload is the JSON event payload.
	Payload = "payload"

	// WithReply indicates if the resolver is waiting for workflow IDs.
	WithReply = "with_reply"

	// RetryCount is the number of failed publish attempts.
	RetryCount = "retry_count"

	// LastError is the error message from the last failed attempt.
	LastError = "last_error"

	// DeadAt is the timestamp when the entry was marked as dead/unrecoverable.
	DeadAt = "dead_at"

	// NextRetryAt is the earliest timestamp at which a failed entry may be retried.
	// NULL means immediately eligible (new entries or never failed).
	NextRetryAt = "next_retry_at"

	// EntityType is the Ent schema name (e.g., "Item", "Location") for filtering.
	EntityType = "entity_type"

	// EntityID is the entity UUID for filtering.
	EntityID = "entity_id"

	// TenantID is the tenant UUID for filtering.
	TenantID = "tenant_id"
)
