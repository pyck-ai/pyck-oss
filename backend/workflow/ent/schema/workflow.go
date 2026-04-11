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
)

// Workflow holds the schema definition for the Workflow entity.
type Workflow struct {
	ent.Schema
}

func (Workflow) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entgql.Type("Workflow"),
		entsql.Schema("workflow"),
		entsql.Table("workflows"),
		entgql.RelayConnection(),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

// Fields of the Workflow.
func (Workflow) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuidgql.GenerateV7UUID).
			Unique().
			Immutable(),
		field.String("name").
			NotEmpty().
			Immutable().
			Annotations(
				entgql.OrderField("NAME"),
			),
		field.String("task_queue").
			NotEmpty().
			Immutable().
			Annotations(
				entgql.OrderField("TASK_QUEUE"),
			),
	}
}

func (Workflow) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("workflowSignals", WorkflowSignal.Type).
			Annotations(
				entgql.RelayConnection(),
				entgql.MapsTo("workflowSignals"),
			),
	}
}

func (Workflow) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "name").
			Unique().
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()),
		index.Fields("tenant_id", "name", "task_queue").
			Unique().
			Annotations(mixin.HistoryMixinNotDeletedIndexAnnotation()),
	}
}

func (Workflow) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.TenantMixin{},
		mixin.DataMixin{},
		mixin.HistoryMixin{},
		mixin.LimitMixin{},
	}
}
