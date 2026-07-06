package resolvers

import (
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	entpredicate "github.com/pyck-ai/pyck/backend/inventory/ent/gen/predicate"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// latestStockPredicate keeps only the latest stock row per (repository_id,
// item_id) committed strictly before cutoff. It expresses the same "latest as
// of" deduplication as the generated DistinctOnExists, but as a correlated
// NOT EXISTS predicate that is evaluated together with the surrounding query's
// own filters (e.g. itemID) and its explicit pagination limit.
//
// This avoids pre-computing the latest-row ids in a separate, tenant-wide query:
// that query carries no explicit limit, so the LimitMixin interceptor silently
// caps it at the default limit with no ORDER BY. On tenants with more (item,
// repository) pairs than the cap, the requested rows fall outside the arbitrary
// capped set and the follow-up itemID filter matches nothing, returning 0 rows.
func latestStockPredicate(cutoff time.Time, tenantIDs []uuid.UUID) entpredicate.Stock {
	// Filters scoping the correlated subquery, mirroring how DistinctOnExists
	// propagates them into its NOT EXISTS check.
	subFilters := []entpredicate.Stock{entstock.CreatedAtLT(cutoff)}
	if len(tenantIDs) > 0 {
		subFilters = append(subFilters, entstock.TenantIDIn(tenantIDs...))
	}

	return func(s *sql.Selector) {
		s2 := sql.Table(entstock.Table).As("s2")
		sub := sql.SelectExpr(sql.Expr("1")).
			From(s2).
			Where(sql.And(
				sql.ColumnsEQ(s2.C(entstock.FieldRepositoryID), s.C(entstock.FieldRepositoryID)),
				sql.ColumnsEQ(s2.C(entstock.FieldItemID), s.C(entstock.FieldItemID)),
				sql.ColumnsGT(s2.C(entstock.FieldCreatedAt), s.C(entstock.FieldCreatedAt)),
			))
		for _, f := range subFilters {
			f(sub)
		}
		s.Where(sql.Not(sql.Exists(sub)))
	}
}
