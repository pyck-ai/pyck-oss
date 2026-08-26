package resolvers

import (
	"context"

	"github.com/google/uuid"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entitemmovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/itemmovement"
	entreplenishmentorderitem "github.com/pyck-ai/pyck/backend/inventory/ent/gen/replenishmentorderitem"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	entrepositorymovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repositorymovement"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
	enttransaction "github.com/pyck-ai/pyck/backend/inventory/ent/gen/transaction"
)

// validateRepositoryEdgeOwnership rejects a repository create/update whose edge
// IDs reference rows outside the caller's tenant. Like the item guard, this
// closes the gap where Ent's tenant privacy interceptor scopes the repository
// mutation but not the entities its edges attach: attaching another tenant's
// stock, transaction, item movement, or repository movement repoints that row's
// repository FK, and attaching another tenant's repository as a child repoints
// its parent_id. IDs are grouped by target entity — the several movement edges
// share one entity type — so one check per entity reports the offending edge.
func validateRepositoryEdgeOwnership(
	ctx context.Context,
	tx *ent.Tx,
	tenantID uuid.UUID,
	itemMovementIDs, repositoryMovementIDs, transactionIDs, stockIDs, childIDs []uuid.UUID,
) error {
	checks := []edgeOwnershipCheck{
		{"itemMovement", itemMovementIDs, func(ids []uuid.UUID) ([]uuid.UUID, error) {
			return tx.ItemMovement.Query().Where(entitemmovement.TenantID(tenantID), entitemmovement.IDIn(ids...)).Limit(len(ids)).IDs(ctx)
		}},
		{"repositoryMovement", repositoryMovementIDs, func(ids []uuid.UUID) ([]uuid.UUID, error) {
			return tx.RepositoryMovement.Query().Where(entrepositorymovement.TenantID(tenantID), entrepositorymovement.IDIn(ids...)).Limit(len(ids)).IDs(ctx)
		}},
		{"repositoryTransaction", transactionIDs, func(ids []uuid.UUID) ([]uuid.UUID, error) {
			return tx.Transaction.Query().Where(enttransaction.TenantID(tenantID), enttransaction.IDIn(ids...)).Limit(len(ids)).IDs(ctx)
		}},
		{"repositoryStock", stockIDs, func(ids []uuid.UUID) ([]uuid.UUID, error) {
			return tx.Stock.Query().Where(entstock.TenantID(tenantID), entstock.IDIn(ids...)).Limit(len(ids)).IDs(ctx)
		}},
		{"child", childIDs, func(ids []uuid.UUID) ([]uuid.UUID, error) {
			return tx.Repository.Query().Where(entrepository.TenantID(tenantID), entrepository.IDIn(ids...)).Limit(len(ids)).IDs(ctx)
		}},
	}
	return ensureEdgesOwned(checks)
}

// validateReplenishmentOrderEdgeOwnership rejects a replenishment-order update
// whose order-item edge IDs reference rows outside the caller's tenant.
// Attaching another tenant's order item repoints its replenishment_order_id FK;
// createReplenishmentOrder is unaffected because it builds its items server-side
// rather than attaching client-supplied IDs.
func validateReplenishmentOrderEdgeOwnership(
	ctx context.Context,
	tx *ent.Tx,
	tenantID uuid.UUID,
	orderItemIDs []uuid.UUID,
) error {
	checks := []edgeOwnershipCheck{
		{"replenishmentOrderItem", orderItemIDs, func(ids []uuid.UUID) ([]uuid.UUID, error) {
			return tx.ReplenishmentOrderItem.Query().Where(entreplenishmentorderitem.TenantID(tenantID), entreplenishmentorderitem.IDIn(ids...)).Limit(len(ids)).IDs(ctx)
		}},
	}
	return ensureEdgesOwned(checks)
}
