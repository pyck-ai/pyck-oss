package resolvers

import (
	"github.com/pyck-ai/pyck/backend/workflow/model"
	"github.com/pyck-ai/pyck/backend/workflow/services"
)

// routingEntryModel converts a stored routing status to its GraphQL type.
func routingEntryModel(status services.RoutingStatus) *model.RoutingEntry {
	targets := make([]*model.RoutingTarget, 0, len(status.Targets))
	for _, t := range status.Targets {
		targets = append(targets, &model.RoutingTarget{
			Kind:       routingTargetKind(t.Kind),
			Workflow:   nonEmpty(t.Workflow),
			WorkflowID: nonEmpty(t.WorkflowID),
			RunID:      nonEmpty(t.RunID),
			Signal:     nonEmpty(t.Signal),
			Reason:     nonEmpty(t.Reason),
			Error:      nonEmpty(t.Error),
		})
	}

	outcome := model.RoutingOutcomeDone
	if status.Outcome == services.RoutingOutcomeGaveUp {
		outcome = model.RoutingOutcomeGaveUp
	}

	return &model.RoutingEntry{
		TenantID:   status.TenantID,
		EventID:    status.EventID,
		Outcome:    outcome,
		Sequence:   status.Sequence,
		Targets:    targets,
		RecordedAt: status.RecordedAt,
	}
}

func routingTargetKind(kind services.RoutingTargetKind) model.RoutingTargetKind {
	switch kind {
	case services.RoutingTargetStarted:
		return model.RoutingTargetKindStarted
	case services.RoutingTargetAlreadyRunning:
		return model.RoutingTargetKindAlreadyRunning
	case services.RoutingTargetSignalled:
		return model.RoutingTargetKindSignalled
	case services.RoutingTargetDropped:
		return model.RoutingTargetKindDropped
	default:
		return model.RoutingTargetKindFailed
	}
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}
