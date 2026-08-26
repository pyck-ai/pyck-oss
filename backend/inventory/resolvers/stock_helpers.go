package resolvers

import (
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entpredicate "github.com/pyck-ai/pyck/backend/inventory/ent/gen/predicate"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// stockHeadPredicate keeps only the current row per (repository_id, item_id):
// the one no row in scope supersedes by version — the only total order per
// pair, unlike created_at (per-pod wall clock).
//
// scope bounds which rows may supersede, so it carries identity and cutoff
// predicates only; value predicates (quantity, …) belong on the outer query,
// or a superseded positive row would shadow the current zero row (#1346).
//
// Correlated NOT EXISTS rather than a pre-computed id list: that list carries
// no explicit limit, so LimitMixin caps it silently and pairs past the cap
// disappear.
func stockHeadPredicate(scope ...entpredicate.Stock) entpredicate.Stock {
	return func(s *sql.Selector) {
		s2 := sql.Table(entstock.Table).As("s2")
		sub := sql.SelectExpr(sql.Expr("1")).
			From(s2).
			Where(sql.And(
				sql.ColumnsEQ(s2.C(entstock.FieldRepositoryID), s.C(entstock.FieldRepositoryID)),
				sql.ColumnsEQ(s2.C(entstock.FieldItemID), s.C(entstock.FieldItemID)),
				sql.ColumnsGT(s2.C(entstock.FieldVersion), s.C(entstock.FieldVersion)),
			))
		for _, f := range scope {
			f(sub)
		}
		s.Where(sql.Not(sql.Exists(sub)))
	}
}

// latestStockPredicate is stockHeadPredicate scoped to one tenant set and to
// rows committed strictly before cutoff, so a time-travel query returns each
// pair's as-of row instead of dropping pairs changed after it.
func latestStockPredicate(cutoff time.Time, tenantIDs []uuid.UUID) entpredicate.Stock {
	scope := []entpredicate.Stock{entstock.CreatedAtLT(cutoff)}
	if len(tenantIDs) > 0 {
		scope = append(scope, entstock.TenantIDIn(tenantIDs...))
	}

	return stockHeadPredicate(scope...)
}

// stockHeadScopePredicates maps a where input to the scope that defines the
// head-row universe: tenant, repository, item and the created_at cutoff.
// Tenant always comes from the request.
func stockHeadScopePredicates(where *ent.StockWhereInput, tenantIDs []uuid.UUID) []entpredicate.Stock {
	scope := []entpredicate.Stock{entstock.TenantIDIn(tenantIDs...)}
	if where == nil {
		return scope
	}

	if where.RepositoryID != nil {
		scope = append(scope, entstock.RepositoryID(*where.RepositoryID))
	}
	if len(where.RepositoryIDIn) > 0 {
		scope = append(scope, entstock.RepositoryIDIn(where.RepositoryIDIn...))
	}
	if where.ItemID != nil {
		scope = append(scope, entstock.ItemID(*where.ItemID))
	}
	if len(where.ItemIDIn) > 0 {
		scope = append(scope, entstock.ItemIDIn(where.ItemIDIn...))
	}
	// The Time where-input resolver maps time → CreatedAtLT.
	if where.CreatedAtLT != nil {
		scope = append(scope, entstock.CreatedAtLT(*where.CreatedAtLT))
	}
	if where.CreatedAtLTE != nil {
		scope = append(scope, entstock.CreatedAtLTE(*where.CreatedAtLTE))
	}

	return scope
}
