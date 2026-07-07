package resolvers

import (
	"context"

	"github.com/google/uuid"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entpredicate "github.com/pyck-ai/pyck/backend/inventory/ent/gen/predicate"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

func latestStockIDs(ctx context.Context, query *ent.StockQuery, preds ...entpredicate.Stock) ([]uuid.UUID, error) {
	// Supersession between rows of the same (repository, item) is decided by
	// version, the only total order per pair; created_at is the writing pod's
	// wall clock and can disagree with it. Callers' cutoff predicates stay on
	// created_at (the "as of" contract is wall-clock time).
	return query.
		Where(preds...).
		DistinctOnExists(
			[]string{entstock.FieldRepositoryID, entstock.FieldItemID},
			entstock.FieldVersion,
			preds...,
		).
		IDs(ctx)
}
