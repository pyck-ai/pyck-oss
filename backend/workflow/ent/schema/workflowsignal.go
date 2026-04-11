package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
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
	return []ent.Index{}
}

func (WorkflowSignal) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
