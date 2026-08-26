package resolvers

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entitemmovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/itemmovement"
	entitemset "github.com/pyck-ai/pyck/backend/inventory/ent/gen/itemset"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
	enttransaction "github.com/pyck-ai/pyck/backend/inventory/ent/gen/transaction"
)

// ErrEdgeNotInTenant is returned when an item create/update references an edge
// ID that does not resolve within the caller's tenant.
var ErrEdgeNotInTenant = errors.New("edge reference not found in tenant")

// edgeOwnershipCheck pairs an edge's requested IDs with a tenant-scoped query
// that reports which of them exist in the caller's tenant.
type edgeOwnershipCheck struct {
	edge  string
	ids   []uuid.UUID
	owned func([]uuid.UUID) ([]uuid.UUID, error)
}

// validateItemEdgeOwnership rejects an item create/update whose edge IDs
// reference rows outside the caller's tenant. Ent's tenant privacy interceptor
// scopes the item mutation itself, but it does not evaluate the privacy policy
// of the entities an edge attaches — so without this check a caller could link
// or repoint another tenant's item sets, stocks, movements, or transactions
// through the item edges. Each ID set is verified against its entity type
// scoped to tenantID; any ID that does not resolve in the tenant fails the
// mutation. Mirrors the ownership check createInventoryItemSet already performs
// on its itemIDs.
func validateItemEdgeOwnership(
	ctx context.Context,
	tx *ent.Tx,
	tenantID uuid.UUID,
	itemSetIDs, stockIDs, movementIDs, transactionIDs []uuid.UUID,
) error {
	checks := []edgeOwnershipCheck{
		{"itemSet", itemSetIDs, func(ids []uuid.UUID) ([]uuid.UUID, error) {
			return tx.ItemSet.Query().Where(entitemset.TenantID(tenantID), entitemset.IDIn(ids...)).Limit(len(ids)).IDs(ctx)
		}},
		{"itemStock", stockIDs, func(ids []uuid.UUID) ([]uuid.UUID, error) {
			return tx.Stock.Query().Where(entstock.TenantID(tenantID), entstock.IDIn(ids...)).Limit(len(ids)).IDs(ctx)
		}},
		{"itemMovement", movementIDs, func(ids []uuid.UUID) ([]uuid.UUID, error) {
			return tx.ItemMovement.Query().Where(entitemmovement.TenantID(tenantID), entitemmovement.IDIn(ids...)).Limit(len(ids)).IDs(ctx)
		}},
		{"itemTransaction", transactionIDs, func(ids []uuid.UUID) ([]uuid.UUID, error) {
			return tx.Transaction.Query().Where(enttransaction.TenantID(tenantID), enttransaction.IDIn(ids...)).Limit(len(ids)).IDs(ctx)
		}},
	}
	return ensureEdgesOwned(checks)
}

// ensureEdgesOwned runs each ownership check in order and returns the first
// failure, so the caller sees one error per offending edge/ID.
func ensureEdgesOwned(checks []edgeOwnershipCheck) error {
	for _, c := range checks {
		if err := ensureEdgeIDsOwned(c.edge, c.ids, c.owned); err != nil {
			return err
		}
	}
	return nil
}

// ensureEdgeIDsOwned verifies every id in ids resolves within the caller's
// tenant. owned returns the subset of ids that exist in the tenant; any
// requested id missing from that subset is rejected. Set membership (not a
// count comparison) so duplicate input IDs are tolerated. IDs are queried in
// chunks of mixin.Limit: the LimitMixin interceptor rejects any single query
// whose bound exceeds that cap, so a caller attaching more than mixin.Limit
// edge IDs at once must not collapse into one over-limit IN (...) query.
func ensureEdgeIDsOwned(edge string, ids []uuid.UUID, owned func([]uuid.UUID) ([]uuid.UUID, error)) error {
	if len(ids) == 0 {
		return nil
	}

	ownedSet := make(map[uuid.UUID]struct{}, len(ids))
	for chunk := range slices.Chunk(ids, mixin.Limit) {
		found, err := owned(chunk)
		if err != nil {
			return fmt.Errorf("validate %s ownership: %w", edge, err)
		}
		for _, id := range found {
			ownedSet[id] = struct{}{}
		}
	}

	for _, id := range ids {
		if _, ok := ownedSet[id]; !ok {
			return fmt.Errorf("%w: %s %q", ErrEdgeNotInTenant, edge, id)
		}
	}

	return nil
}
