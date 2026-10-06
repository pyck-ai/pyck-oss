package resolvers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entitem "github.com/pyck-ai/pyck/backend/inventory/ent/gen/item"
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

// maxItemStocks bounds InventoryItem.itemstocks: an item stocked in more
// repositories must be read through the paginated currentStocks.
const maxItemStocks = 10_000

// ErrTooManyItemStocks is returned by InventoryItem.itemstocks when the item
// has more than maxItemStocks non-empty current stock rows.
var ErrTooManyItemStocks = errors.New("too many current stock rows")

// currentStockIDs returns the ids of an item's non-empty current rows, ordered
// by repository. It pages by repository_id, unique among current rows: OFFSET
// pages re-run stockHeadPredicate over every skipped row, taking minutes on
// long histories.
func currentStockIDs(ctx context.Context, client *ent.Client, tenantID, itemID uuid.UUID) ([]uuid.UUID, error) {
	scope := []entpredicate.Stock{entstock.TenantID(tenantID), entstock.ItemID(itemID)}

	var ids []uuid.UUID
	after := uuid.Nil
	for {
		var page []struct {
			ID           uuid.UUID `json:"id"`
			RepositoryID uuid.UUID `json:"repository_id"`
		}
		err := client.Stock.Query().
			Where(scope...).
			Where(stockHeadPredicate(scope...), stockNotEmpty()).
			Where(entpredicate.Stock(sql.FieldGT(entstock.FieldRepositoryID, after))).
			Order(ent.Asc(entstock.FieldRepositoryID)).
			Limit(mixin.Limit).
			Select(entstock.FieldID, entstock.FieldRepositoryID).
			Scan(ctx, &page)
		if err != nil {
			return nil, err
		}

		for _, row := range page {
			ids = append(ids, row.ID)
		}
		if len(ids) > maxItemStocks {
			return nil, fmt.Errorf("%w: item %s has more than %d, query currentStocks", ErrTooManyItemStocks, itemID, maxItemStocks)
		}
		if len(page) < mixin.Limit {
			return ids, nil
		}
		after = page[len(page)-1].RepositoryID
	}
}

// stockNotEmpty keeps rows with any nonzero stock value. Outer query only (see
// stockHeadPredicate): in the scope, an emptied repository's older row would
// count as current.
func stockNotEmpty() entpredicate.Stock {
	return entstock.Or(
		entstock.QuantityNEQ(0),
		entstock.IncomingStockNEQ(0),
		entstock.OutgoingStockNEQ(0),
		entstock.OwnQuantityNEQ(0),
		entstock.OwnIncomingStockNEQ(0),
		entstock.OwnOutgoingStockNEQ(0),
	)
}

// itemHasCurrentStockPredicate matches items with at least one non-empty
// current stock row for which every given where input holds (none given: any
// such row). Current means the same as in currentStocks: the highest version
// per (repository, item), with a deleted head hiding its pair.
//
// The EXISTS subquery is hand-built, so no query interceptor runs inside it:
//   - tenant: the outer row and every superseding row correlate on the item's
//     tenant_id, and ScopeNeighborToTenants narrows the outer row like the
//     generated hasItemStocksWith does;
//   - soft delete: only the outer row is filtered (ScopeNeighborToLive).
//     Superseding rows are not, so a deleted head still hides its pair;
//   - value filters (quantity, ...) sit on the outer row only, never in the
//     head scope (see stockHeadPredicate, #1346);
//   - a created_at cutoff in an input (the time filter) also bounds the
//     superseding rows, so an as-of query picks the as-of head.
//
// The stockWhereInputResolver.Time resolver also runs for inputs nested in
// hasCurrentStockWith: it adds CreatedAtLT plus its own head predicate to the
// input's P(), which lands on the outer row "cs" and is correlated to it, so
// it composes inside this subquery. It duplicates the head selection built
// here with the same cutoff, which is redundant but harmless.
func itemHasCurrentStockPredicate(with []*ent.StockWhereInput) (entpredicate.Item, error) {
	preds := make([]entpredicate.Stock, 0, len(with))
	var cutoffs []entpredicate.Stock
	for _, w := range with {
		p, err := w.P()
		if err != nil {
			return nil, err
		}
		preds = append(preds, p)
		if w.CreatedAtLT != nil {
			cutoffs = append(cutoffs, entstock.CreatedAtLT(*w.CreatedAtLT))
		}
		if w.CreatedAtLTE != nil {
			cutoffs = append(cutoffs, entstock.CreatedAtLTE(*w.CreatedAtLTE))
		}
	}

	return func(items *sql.Selector) {
		cs := sql.Table(entstock.Table).As("cs")
		sub := sql.SelectExpr(sql.Expr("1")).
			From(cs).
			WithContext(items.Context()).
			Where(sql.ColumnsEQ(cs.C(entstock.FieldItemID), items.C(entitem.FieldID))).
			Where(sql.ColumnsEQ(cs.C(entstock.FieldTenantID), items.C(entitem.FieldTenantID)))

		entpredicate.ScopeNeighborToTenants(sub)
		entpredicate.ScopeNeighborToLive(sub)

		// Superseding rows: same tenant as the item, plus the cutoffs.
		scope := append([]entpredicate.Stock{func(s2 *sql.Selector) {
			s2.Where(sql.ColumnsEQ(s2.C(entstock.FieldTenantID), items.C(entitem.FieldTenantID)))
		}}, cutoffs...)
		stockHeadPredicate(scope...)(sub)
		stockNotEmpty()(sub)

		for _, p := range preds {
			p(sub)
		}
		items.Where(sql.Exists(sub))
	}, nil
}
