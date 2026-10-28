//nolint:testpackage // in-package test helper shared by the repository-movement suites, which run in-package themselves.
package stock

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// movementItemIDs returns the distinct item ids of every stock row stamped
// with the movement. The rows are paged rather than capped: the assertions
// built on this compare the set against one expected item, and a fan-out
// that re-stamps hundreds of unrelated items must surface as extra ids, not
// vanish behind LimitMixin's cap.
func movementItemIDs(t *testing.T, ctx context.Context, tx *ent.Tx, tenantID, movementID uuid.UUID) []string {
	t.Helper()
	rows, err := tx.Stock.Query().
		Where(entstock.TenantID(tenantID), entstock.MovementID(movementID)).
		Select(entstock.FieldItemID).
		AllPages(ctx, mixin.Limit)
	require.NoError(t, err)

	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if id := row.ItemID.String(); !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}
