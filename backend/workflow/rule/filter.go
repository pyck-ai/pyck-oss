package rule

import (
	"context"
	"time"

	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"

	"github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	"github.com/pyck-ai/pyck/backend/workflow/ent/gen/privacy"
	"github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflow"
)

// WorkflowTenantIDQueryFilter filters all queries for the users tenant id.
func WorkflowTenantIDQueryFilter() privacy.QueryRule {
	return privacy.WorkflowQueryRuleFunc(func(ctx context.Context, q *gen.WorkflowQuery) error {
		req := request.ForContext(ctx)

		if !req.User().IsAuthenticated() {
			return privacy.Deny
		}

		if !req.User().IsSystemUser() {
			q.Where(workflow.TenantIDEQ(req.MutationTenantID()))
		}

		return privacy.Skip
	})
}

// WorkflowTenantIDMutationFilter filters all mutations for the users tenant id.
func WorkflowTenantIDMutationFilter() privacy.MutationRule {
	return privacy.MutationRuleFunc(func(ctx context.Context, m gen.Mutation) error {
		req := request.ForContext(ctx)

		if !req.User().IsAuthenticated() {
			return privacy.Deny
		}

		wm, ok := m.(*gen.WorkflowMutation)
		if !ok {
			return privacy.Denyf("Invalid mutation type")
		}

		wm.Where(workflow.TenantIDEQ(req.MutationTenantID()))

		if tid, ok := wm.TenantID(); ok {
			if tid.String() != req.MutationTenantID().String() {
				return privacy.Denyf("Tenant id is incorrect")
			}
		}

		return privacy.Skip
	})
}

// WorkflowDeletedAtQueryFilter filters all deleted items from queries.
func WorkflowDeletedAtQueryFilter() privacy.QueryRule {
	return privacy.WorkflowQueryRuleFunc(func(ctx context.Context, q *gen.WorkflowQuery) error {
		if !feature.HasFeature(ctx, feature.FEATURE_SHOW_DELETED) {
			q.Where(workflow.DeletedAtEQ(time.Time{}))
		}

		return privacy.Skip
	})
}

// WorkflowDeletedAtMutationFilter filters all deleted items from mutations.
func WorkflowDeletedAtMutationFilter() privacy.MutationRule {
	return privacy.MutationRuleFunc(func(ctx context.Context, m gen.Mutation) error {
		im, ok := m.(*gen.WorkflowMutation)
		if !ok {
			return privacy.Denyf("Invalid mutation type")
		}

		im.Where(workflow.DeletedAtEQ(time.Time{}))

		return privacy.Skip
	})
}

// WorkflowSignalTenantIDQueryFilter filters all queries for the users tenant id.
func WorkflowSignalTenantIDQueryFilter() privacy.QueryRule {
	return privacy.WorkflowSignalQueryRuleFunc(func(ctx context.Context, q *gen.WorkflowSignalQuery) error {
		req := request.ForContext(ctx)

		if !req.User().IsAuthenticated() {
			return privacy.Deny
		}

		if !req.User().IsSystemUser() {
			q.Where(workflowsignal.TenantIDEQ(req.MutationTenantID()))
		}

		return privacy.Skip
	})
}

// WorkflowSignalTenantIDMutationFilter filters all mutations for the users tenant id.
func WorkflowSignalTenantIDMutationFilter() privacy.MutationRule {
	return privacy.MutationRuleFunc(func(ctx context.Context, m gen.Mutation) error {
		req := request.ForContext(ctx)

		if !req.User().IsAuthenticated() {
			return privacy.Deny
		}

		wm, ok := m.(*gen.WorkflowSignalMutation)
		if !ok {
			return privacy.Denyf("Invalid mutation type")
		}

		wm.Where(workflowsignal.TenantIDEQ(req.MutationTenantID()))

		if tid, ok := wm.TenantID(); ok {
			if tid.String() != req.MutationTenantID().String() {
				return privacy.Denyf("Tenant id is incorrect")
			}
		}

		return privacy.Skip
	})
}

// WorkflowSignalDeletedAtQueryFilter filters all deleted items from queries.
func WorkflowSignalDeletedAtQueryFilter() privacy.QueryRule {
	return privacy.WorkflowSignalQueryRuleFunc(func(ctx context.Context, q *gen.WorkflowSignalQuery) error {
		if !feature.HasFeature(ctx, feature.FEATURE_SHOW_DELETED) {
			q.Where(workflowsignal.DeletedAtEQ(time.Time{}))
		}

		return privacy.Skip
	})
}

// WorkflowSignalDeletedAtMutationFilter filters all deleted items from mutations.
func WorkflowSignalDeletedAtMutationFilter() privacy.MutationRule {
	return privacy.MutationRuleFunc(func(ctx context.Context, m gen.Mutation) error {
		im, ok := m.(*gen.WorkflowSignalMutation)
		if !ok {
			return privacy.Denyf("Invalid mutation type")
		}

		im.Where(workflowsignal.DeletedAtEQ(time.Time{}))

		return privacy.Skip
	})
}
