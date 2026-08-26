package services

import (
	"context"

	"github.com/google/uuid"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
)

// ActiveWorkflowsWithSignals exposes the router's live-subscription filter to
// the external test package.
func (wr *SignalRouter) ActiveWorkflowsWithSignals(ctx context.Context, tenantID uuid.UUID) ([]*ent.Workflow, error) {
	return wr.activeWorkflowsWithSignals(ctx, tenantID)
}
