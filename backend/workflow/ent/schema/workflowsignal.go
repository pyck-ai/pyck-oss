package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
	"github.com/pyck-ai/pyck/backend/common/workflow"
)

// WorkflowSignal holds the schema definition for the WorkflowSignal entity.
type WorkflowSignal struct {
	ent.Schema
}

func (WorkflowSignal) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entgql.Type("WorkflowSignal"),
		entsql.Schema("workflow"),
		entsql.Table("workflow-signals"),
		entgql.RelayConnection(),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

// Fields of the Workflow.
func (WorkflowSignal) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.UUID("workflow_id", uuid.UUID{}).
			Annotations(
				entgql.OrderField("WORKFLOW_ID"),
			),
		field.String("nats_topic").
			NotEmpty().
			Annotations(
				entgql.OrderField("NATS_TOPIC"),
			),
		field.String("temporal_signal").
			Optional().
			Annotations(
				entgql.OrderField("TEMPORAL_SIGNAL"),
			),
		field.Enum("temporal_signal_type").
			Values(workflow.SignalTypeStrings()...).
			Annotations(
				entgql.OrderField("TEMPORAL_SIGNAL_TYPE"),
				entgql.Type("WorkflowSignalType"),
			),
		field.String("filter_rule").
			Optional().
			Annotations(
				entgql.OrderField("FILTER_RULE"),
			),
		// worker_id identifies the worker instance that owns this subscription.
		// Always set: every subscription belongs to exactly one worker. Internal
		// bookkeeping only, so it is hidden from the GraphQL API.
		field.String("worker_id").
			NotEmpty().
			Immutable().
			Annotations(entgql.Skip()),
		// expires_at is when the subscription goes stale absent a refresh from
		// its worker. Always set. Hidden from the GraphQL API.
		field.Time("expires_at").
			Annotations(entgql.Skip()),
		// stopped_at is set when the owning worker shut down cleanly
		// (unregisterWorker) and cleared by that worker's next registerWorkflow.
		// It is a hint, not a deletion: the router prefers rows with no
		// stopped_at and only falls back to stopped rows until their TTL lapses.
		// Hidden from the GraphQL API.
		field.Time("stopped_at").
			Optional().
			Nillable().
			Annotations(entgql.Skip()),
	}
}

func (WorkflowSignal) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("workflow", Workflow.Type).
			Ref("workflowSignals").
			Field("workflow_id").
			Required().
			Unique(),
	}
}

func (WorkflowSignal) Indexes() []ent.Index {
	return []ent.Index{
		// One live subscription per worker and signal identity. worker_id scopes
		// the key so concurrent workers registering the same workflow no longer
		// contend on each other's rows.
		index.Fields("tenant_id", "workflow_id", "worker_id", "nats_topic", "temporal_signal_type", "temporal_signal").
			Unique().
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()),
		// Supports the janitor's sweep of expired subscriptions.
		index.Fields("expires_at"),
		// Supports unregisterWorker, which looks up one worker's subscriptions.
		index.Fields("tenant_id", "worker_id"),
	}
}

func (WorkflowSignal) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
