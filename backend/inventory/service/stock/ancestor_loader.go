package stock

import (
	"context"
	"fmt"
	"strings"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// stockKey identifies a (repository, item) pair in the loaded map. It is
// the key shape returned by loadAncestorStocks so callers can address the
// latest stock row per pair without nesting another map.
type stockKey struct {
	RepositoryID uuid.UUID
	ItemID       uuid.UUID
}

// ancestorWalkDepthCap bounds the recursive CTE walk. The domain rule
// (FINDINGS section 0.4 / TODO.md section 0.4) is "no hard repo depth
// limit, assume 20 max, typically ~10", but the regression test suite
// intentionally exercises 50-deep chains (resolvers/testdata/stock/
// deep-nesting-50-levels.test.yaml) to pin the LCA classification at
// extreme depth. Phase 4.3 made the loader an authoritative input to
// the executor walks — a too-low cap silently drops ancestors and the
// executor then errors with errAncestorRepoNotPreloaded — so the cap
// must comfortably exceed the deepest test fixture. We pick 100, which
// is 5x the documented "assume 20 max" while still being a finite
// belt-and-braces guard against a corrupt parent_id cycle.
const ancestorWalkDepthCap = 100

// loadAncestorStocks returns the union of seed repos and all their
// ancestors via parent_id, plus the latest stock row per (repo, item)
// pair for those repos. The implementation is "Approach 1" from
// TODO.md Step 4.1: one recursive CTE that returns only repository IDs,
// then two ent queries hydrate typed ent.Repository / ent.Stock structs.
// Two round trips, but typed and safe — column ordering is enforced by
// ent's generated scanners, not by hand.
//
// The recursive walk is depth-capped at ancestorWalkDepthCap (raised
// to 100 in Phase 4.3 to cover the deep-nesting-50-levels regression
// fixture). Seeds whose tenant_id does not match are filtered at the
// seed step; their ancestors are filtered at every recursive step. A
// cyclic parent_id graph terminates at the depth cap rather than
// looping forever.
//
// includeDeleted mirrors the existing showDeletedCtx toggle in
// GetRepositoriesDetails / GetCurrentRepositoriesStock. When false,
// soft-deleted repositories (deleted_at IS NOT NULL) are excluded from
// both the recursive walk and the hydration query, and soft-deleted
// stock rows are likewise excluded. When true, all rows are visible.
//
// items, when non-empty, narrows the stock map to those items. When
// empty, every item that has a row at any returned repo is loaded.
//
// The returned maps key on:
//   - repos: repository ID
//   - stocks: (repository ID, item ID)
//
// Both maps are non-nil even when the corresponding result set is
// empty, so callers can read freely without nil-map panics.
func (s *service) loadAncestorStocks(
	ctx context.Context,
	tx *ent.Tx,
	tenantID uuid.UUID,
	seeds []uuid.UUID,
	items []uuid.UUID,
	includeDeleted bool,
) (map[uuid.UUID]ent.Repository, map[stockKey]ent.Stock, error) {
	repos := map[uuid.UUID]ent.Repository{}
	stocks := map[stockKey]ent.Stock{}

	if len(seeds) == 0 {
		return repos, stocks, nil
	}

	// Step 1: gather the set of ancestor IDs via a single recursive
	// CTE. We deliberately return only IDs from the raw query — the
	// hydration into typed ent.Repository / ent.Stock happens in
	// follow-up ent queries so we keep column ordering safety.
	ids, err := s.loadAncestorIDs(ctx, tx, tenantID, seeds, includeDeleted)
	if err != nil {
		return nil, nil, err
	}
	if len(ids) == 0 {
		return repos, stocks, nil
	}

	// Step 2: hydrate Repository structs via the typed query builder.
	// The TenantMixin query interceptor adds the tenant filter at the
	// privacy layer; HistoryMixin's filter is bypassed by the
	// includeDeleted-marked context, but we still pass an explicit
	// DeletedAtIsNil predicate when includeDeleted is false so the
	// guarantee is independent of which interceptors fire.
	repoQuery := tx.Repository.Query().Where(entrepository.IDIn(ids...))
	if !includeDeleted {
		repoQuery = repoQuery.Where(entrepository.DeletedAtIsNil())
	}
	repoRows, err := repoQuery.AllPages(ctx, mixin.Limit)
	if err != nil {
		return nil, nil, fmt.Errorf("loadAncestorStocks: hydrate repositories: %w", err)
	}
	for _, r := range repoRows {
		repos[r.ID] = *r
	}

	// Step 3: hydrate the current Stock row per (repo, item) for the
	// gathered repo set, optionally narrowed to items. "Current" is the
	// highest version (UNIQUE and monotonic per tenant/repo/item), not the
	// latest created_at: created_at is the writing pod's wall clock and not a
	// total order across workers, so it can return a superseded row. Mirrors
	// loadLatestStockPerRepo and create_item_movement_proc (ORDER BY version).
	latestPredicate := func(sel *sql.Selector) {
		t := sql.Table(entstock.Table).As("s2")
		sub := sql.SelectExpr(sql.Expr("1")).From(t).Where(sql.And(
			sql.ColumnsEQ(t.C(entstock.RepositoryColumn), sel.C(entstock.RepositoryColumn)),
			sql.ColumnsEQ(t.C(entstock.ItemColumn), sel.C(entstock.ItemColumn)),
			sql.ColumnsGT(t.C(entstock.FieldVersion), sel.C(entstock.FieldVersion)),
		))
		sel.Where(sql.Not(sql.Exists(sub)))
	}

	stockQuery := tx.Stock.Query().
		Where(
			entstock.TenantID(tenantID),
			entstock.RepositoryIDIn(ids...),
		).
		Where(func(sel *sql.Selector) { latestPredicate(sel) })
	if len(items) > 0 {
		stockQuery = stockQuery.Where(entstock.ItemIDIn(items...))
	}
	if !includeDeleted {
		stockQuery = stockQuery.Where(entstock.DeletedAtIsNil())
	}

	stockRows, err := stockQuery.AllPages(ctx, mixin.Limit)
	if err != nil {
		return nil, nil, fmt.Errorf("loadAncestorStocks: hydrate stocks: %w", err)
	}
	for _, r := range stockRows {
		stocks[stockKey{RepositoryID: r.RepositoryID, ItemID: r.ItemID}] = *r
	}

	return repos, stocks, nil
}

// loadItemIDsAtRepo returns the set of item IDs that currently hold a
// stock row at the given repository for the given tenant. "Currently"
// matches the same "latest per (repo, item)" semantics loadAncestorStocks
// uses for its stock hydration: a row qualifies when no row at the
// same (repo, item) has a strictly greater version. Soft-deleted
// rows are excluded.
//
// CreateRepositoryMovement uses this to pre-compute the items
// physically present on the moving repository so the subsequent
// loadAncestorStocks call can narrow the closure read via its items
// filter. Without this narrowing the closure load reads every
// (repo, item) pair in the ancestor closure (items=nil), which on
// tenants whose stocks history has accumulated rich unrelated rows
// at a shared ancestor amplifies the read by orders of magnitude —
// the fan-out then discards everything but the items at the moving
// repository anyway. Mirrors how DeleteRepositoryMovement passes
// originalQuantities keys to loadAncestorStocks at impl.go:1278-1282
// for the same reason.
//
// The returned slice has no defined order; deduplicated by the
// latest-row predicate (each (repo, item) returns at most one row).
func (s *service) loadItemIDsAtRepo(
	ctx context.Context,
	tx *ent.Tx,
	tenantID uuid.UUID,
	repoID uuid.UUID,
) ([]uuid.UUID, error) {
	// Highest-version row per (repo, item), identical to loadAncestorStocks
	// and loadLatestStockPerRepo. version is UNIQUE and monotonic per
	// (tenant, repo, item), so the three call sites return exactly the same
	// current row (created_at is not a total order across workers).
	latestPredicate := func(sel *sql.Selector) {
		t := sql.Table(entstock.Table).As("s2")
		sub := sql.SelectExpr(sql.Expr("1")).From(t).Where(sql.And(
			sql.ColumnsEQ(t.C(entstock.RepositoryColumn), sel.C(entstock.RepositoryColumn)),
			sql.ColumnsEQ(t.C(entstock.ItemColumn), sel.C(entstock.ItemColumn)),
			sql.ColumnsGT(t.C(entstock.FieldVersion), sel.C(entstock.FieldVersion)),
		))
		sel.Where(sql.Not(sql.Exists(sub)))
	}

	records, err := tx.Stock.Query().
		Where(
			entstock.TenantID(tenantID),
			entstock.RepositoryID(repoID),
			entstock.DeletedAtIsNil(),
		).
		Where(latestPredicate).
		AllPages(ctx, mixin.Limit)
	if err != nil {
		return nil, fmt.Errorf("loadItemIDsAtRepo: %w", err)
	}

	out := make([]uuid.UUID, 0, len(records))
	for _, r := range records {
		out = append(out, r.ItemID)
	}
	return out, nil
}

// loadAncestorIDs runs the recursive parent_id walk against the
// repositories table and returns the deduplicated set of IDs that
// belong to the seed-plus-ancestors closure. The returned slice has
// stable order only insofar as the underlying engine's DISTINCT yields
// a stable order — callers must not depend on it.
//
// The query uses sequential positional placeholders ($1, $2, ...)
// without skipping any index. Both pgx and the mattn/go-sqlite3 driver
// accept this form; SQLite's bind path matches $NNN by its numeric
// suffix and skipping a number (e.g., declaring $3 without $2) breaks
// the binding. The includeDeleted toggle is therefore expressed by
// switching the SQL itself — a soft-delete branch and an
// include-everything branch — rather than by passing a bool param.
func (s *service) loadAncestorIDs(
	ctx context.Context,
	tx *ent.Tx,
	tenantID uuid.UUID,
	seeds []uuid.UUID,
	includeDeleted bool,
) ([]uuid.UUID, error) {
	// Build the IN (...) placeholder list. $1 is the tenant ID and
	// $2..$(N+1) are the seed IDs. There is no $0 and no gap so that
	// SQLite's positional-name binder (which matches by the trailing
	// integer in the placeholder text) lines up with the args slice.
	const tenantPlaceholderPos = 1
	seedPlaceholders := make([]string, len(seeds))
	args := make([]any, 0, 1+len(seeds))
	args = append(args, tenantID)
	for i, seed := range seeds {
		seedPlaceholders[i] = fmt.Sprintf("$%d", tenantPlaceholderPos+i+1)
		args = append(args, seed)
	}

	// deletedPredicate is empty when soft-deletes are visible (the
	// caller asked to include them) and adds an "AND deleted_at IS
	// NULL" filter at both legs of the UNION ALL otherwise. We toggle
	// via SQL rather than via a bool parameter because SQLite's mattn
	// driver binds positional placeholders by the trailing integer in
	// the placeholder text — passing a parameter that the prepared
	// statement does not actually reference shifts the remaining
	// bindings off by one and produces silently empty results.
	deletedClauseAnchor := ""
	deletedClauseRecursive := ""
	if !includeDeleted {
		deletedClauseAnchor = fmt.Sprintf(" AND %s IS NULL", entrepository.FieldDeletedAt)
		deletedClauseRecursive = fmt.Sprintf(" AND r.%s IS NULL", entrepository.FieldDeletedAt)
	}

	// The CTE keeps the table name unqualified (repositories rather
	// than inventory.repositories) so the same SQL works under PG with
	// search_path=inventory (production) and under SQLite (tests),
	// where there is no schema namespace at all. Tenant-, deleted- and
	// depth-filters are repeated on the recursive step so a cyclic or
	// sibling-shaped parent_id graph cannot leak rows from outside the
	// tenant or below the soft-delete cutoff.
	query := fmt.Sprintf(`WITH RECURSIVE ancestors(id, parent_id, depth) AS (
  SELECT %[1]s, %[2]s, 0
  FROM %[3]s
  WHERE %[4]s = $1 AND %[1]s IN (%[5]s)%[6]s
  UNION ALL
  SELECT r.%[1]s, r.%[2]s, a.depth + 1
  FROM %[3]s r
  JOIN ancestors a ON r.%[1]s = a.parent_id
  WHERE r.%[4]s = $1 AND a.depth < %[7]d%[8]s
)
SELECT DISTINCT id FROM ancestors`,
		entrepository.FieldID,
		entrepository.FieldParentID,
		entrepository.Table,
		entrepository.FieldTenantID,
		strings.Join(seedPlaceholders, ", "),
		deletedClauseAnchor,
		ancestorWalkDepthCap,
		deletedClauseRecursive,
	)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("loadAncestorStocks: ancestor CTE: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("loadAncestorStocks: scan ancestor row: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("loadAncestorStocks: iterate ancestor rows: %w", err)
	}
	return ids, nil
}

// nestStockKeyMap converts the flat stockKey-keyed map returned by
// loadAncestorStocks into the nested map[repoID]map[itemID]Stock shape
// that the legacy simulate / insertStockMap helpers expect. It is a
// temporary call-site adapter introduced in Phase 4.2 so the loader
// can replace the GetRepositoriesDetails + GetCurrentRepositoriesStock
// pair without touching the executor helpers; Phase 4.3 will refactor
// those helpers to consume the flat shape directly and this adapter
// will be deleted.
func nestStockKeyMap(flat map[stockKey]ent.Stock) map[uuid.UUID]map[uuid.UUID]ent.Stock {
	nested := make(map[uuid.UUID]map[uuid.UUID]ent.Stock, len(flat))
	for k, v := range flat {
		inner, ok := nested[k.RepositoryID]
		if !ok {
			inner = make(map[uuid.UUID]ent.Stock)
			nested[k.RepositoryID] = inner
		}
		inner[k.ItemID] = v
	}
	return nested
}
