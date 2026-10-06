package stock

import (
	"context"

	"github.com/google/uuid"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entitem "github.com/pyck-ai/pyck/backend/inventory/ent/gen/item"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
)

// The helpers below load a row a movement is about to reference, restricted
// to the movement's tenant. The tenant privacy filter scopes the movement row
// itself but not the ids it stores, and the system user skips that filter
// altogether, so the tenant is named explicitly. A row of another tenant, or
// one that does not exist, fails with the same ent not-found error, so the
// refusal does not reveal which of the two it was.

// requireItemInTenant fails unless itemID names an item of tenantID.
func requireItemInTenant(ctx context.Context, tx *ent.Tx, tenantID, itemID uuid.UUID) error {
	_, err := tx.Item.Query().
		Where(entitem.ID(itemID), entitem.TenantID(tenantID)).
		OnlyID(ctx)
	return err
}

// repositoryInTenant returns repository id if it belongs to tenantID.
func repositoryInTenant(ctx context.Context, tx *ent.Tx, tenantID, id uuid.UUID) (*ent.Repository, error) {
	return tx.Repository.Query().
		Where(entrepository.ID(id), entrepository.TenantID(tenantID)).
		Only(ctx)
}
