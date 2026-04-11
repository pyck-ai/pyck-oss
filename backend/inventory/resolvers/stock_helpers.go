package resolvers

import (
	"context"

	"github.com/google/uuid"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entpredicate "github.com/pyck-ai/pyck/backend/inventory/ent/gen/predicate"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

func latestStockIDs(ctx context.Context, query *ent.StockQuery, preds ...entpredicate.Stock) ([]uuid.UUID, error) {
	return query.
		Where(preds...).
		DistinctOnExists(
			[]string{entstock.FieldRepositoryID, entstock.FieldItemID},
			entstock.FieldCreatedAt,
			preds...,
		).
		IDs(ctx)
}
