package mixin

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
	"github.com/google/uuid"

	outboxfields "github.com/pyck-ai/pyck/backend/common/internal/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

// nowUTC returns the current time in UTC.
func nowUTC() time.Time { return time.Now().UTC() }

// OutboxMixin provides the event_outbox table schema for the Transactional Outbox Pattern.
//
// Field Names: The database column names defined by this mixin are the canonical
// names for the outbox table. These constants are available in the
// backend/common/internal/ent/mixin package (outbox_fieldnames.go) for use
// in common code that builds SQL queries. Service code should use the generated
// entityeventsoutbox.Field* constants instead.
//
// Each service should create an EntityEventsOutbox entity that uses this mixin:
//
//	type EntityEventsOutbox struct {
//	    ent.Schema
//	}
//
//	func (EntityEventsOutbox) Mixin() []ent.Mixin {
//	    return []ent.Mixin{
//	        mixin.OutboxMixin{},
//	    }
//	}
//
//	func (EntityEventsOutbox) Annotations() []schema.Annotation {
//	    return []schema.Annotation{
//	        entsql.Schema("<service>"),
//	        entsql.Table("event_outbox"),
//	    }
//	}
//
// The outbox handler reads unpublished entries (published_at IS NULL), publishes them to NATS,
// and updates published_at on success.
type OutboxMixin struct {
	mixin.Schema
}

// Fields returns the outbox table fields.
func (OutboxMixin) Fields() []ent.Field {
	return []ent.Field{
		// Primary key (UUID v7 for time-ordering)
		field.UUID(outboxfields.ID, uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),

		// Timestamp when the outbox entry was created (immutable)
		field.Time(outboxfields.CreatedAt).
			Default(nowUTC).
			Immutable(),

		// Timestamp when the event was successfully published to NATS
		// NULL indicates the event has not been published yet
		field.Time(outboxfields.PublishedAt).
			Optional().
			Nillable(),

		// User ID of the actor who triggered the mutation (for audit)
		// NULL for system-triggered mutations
		field.UUID(outboxfields.UserID, uuid.UUID{}).
			Optional().
			Nillable(),

		// Correlation ID for tracing across services
		// Derived from OpenTelemetry trace ID or explicitly set
		// Used in NATS message ID for idempotent publishing: <correlation_id>-<entity_type>-<entity_id>
		field.String(outboxfields.CorrelationID).
			NotEmpty(),

		// NATS topic to publish the event to
		field.String(outboxfields.Topic).
			NotEmpty(),

		// Event payload as JSON (MutationEventMessage with before/after state)
		field.JSON(outboxfields.Payload, map[string]any{}).
			SchemaType(map[string]string{
				dialect.Postgres: "jsonb",
			}),

		// Whether the resolver is waiting for workflow IDs in response
		// When true, the outbox handler should wait for NATS reply and
		// deliver workflow details back to the resolver via ReplyRegistry
		field.Bool(outboxfields.WithReply).
			Default(false),

		// Number of failed publish attempts
		// Used for retry logic and monitoring
		field.Int(outboxfields.RetryCount).
			Default(0),

		// Last error message from failed publish attempt
		// NULL when no error or after successful publish
		field.String(outboxfields.LastError).
			Optional().
			Nillable(),

		// Timestamp when the entry was marked as dead (unrecoverable)
		// NULL indicates the entry is still active
		// Set when max retries exceeded or correlation group is poisoned
		field.Time(outboxfields.DeadAt).
			Optional().
			Nillable(),

		// Earliest time at which a failed entry may next be retried (exponential backoff).
		// NULL = eligible immediately. Set by SQL on failure as NOW() + 2^retry_count seconds.
		field.Time(outboxfields.NextRetryAt).
			Optional().
			Nillable(),

		// Ent schema name (e.g., "Item", "Location") extracted from payload for filtering.
		field.String(outboxfields.EntityType).
			Optional().
			Nillable(),

		// Entity UUID extracted from payload for filtering.
		field.UUID(outboxfields.EntityID, uuid.UUID{}).
			Optional().
			Nillable(),

		// Tenant UUID extracted from payload for filtering.
		field.UUID(outboxfields.TenantID, uuid.UUID{}).
			Optional().
			Nillable(),
	}
}

// outboxPendingWhere is the partial index predicate for rows not yet published or dead.
var outboxPendingWhere = outboxfields.PublishedAt + " IS NULL AND " + outboxfields.DeadAt + " IS NULL"

// Indexes returns the outbox table indexes.
func (OutboxMixin) Indexes() []ent.Index {
	return []ent.Index{
		// Polling index: used by step 1 of the selector to find ready entries.
		// (next_retry_at NULLS FIRST, correlation_id, created_at) WHERE published_at IS NULL AND dead_at IS NULL
		index.Fields(outboxfields.NextRetryAt, outboxfields.CorrelationID, outboxfields.CreatedAt).
			Annotations(entsql.IndexWhere(outboxPendingWhere)),

		// Correlation group index: used by step 2 of the selector (WHERE correlation_id IN (...))
		// and by NewOutboxMarkCorrelationDead (DELETE WHERE correlation_id = ?).
		index.Fields(outboxfields.CorrelationID, outboxfields.CreatedAt).
			Annotations(entsql.IndexWhere(outboxPendingWhere)),

		// Tenant index: filter events by tenant.
		index.Fields(outboxfields.TenantID, outboxfields.CreatedAt).
			Annotations(entsql.IndexWhere(outboxPendingWhere)),

		// Audit index: find events by user.
		index.Fields(outboxfields.UserID, outboxfields.CreatedAt),
	}
}
