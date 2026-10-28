package resolvers

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	managementapi "github.com/pyck-ai/pyck/backend/management/api"
)

// checkAssignee refuses an assignee that is not a live user of tenantID.
// Self-assignment skips the lookup: the caller already passed the writer gate,
// and a user acting here through a role grant has no users row in tenantID.
func (r *Resolver) checkAssignee(ctx context.Context, tenantID, callerID, assigneeID uuid.UUID) error {
	if assigneeID == callerID {
		return nil
	}

	id, tenant := assigneeID.String(), tenantID.String()
	first := 1
	// The service token bypasses management's tenant filter; restate the tenant.
	resp, err := r.mgmtClient.GetUsers(ctx, managementapi.GetUsersArgs{
		First: &first,
		Where: &managementapi.UserWhereInput{ID: &id, TenantID: &tenant},
	})
	if err != nil {
		return fmt.Errorf("look up assignee: %w", err)
	}

	// The users query also returns soft-deleted rows.
	for _, edge := range resp.GetUsers().GetEdges() {
		if node := edge.GetNode(); node != nil && node.DeletedAt == nil {
			return nil
		}
	}
	return ErrAssigneeNotTenantUser
}
