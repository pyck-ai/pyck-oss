package stock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/std"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entitemmovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/itemmovement"
	entpredicate "github.com/pyck-ai/pyck/backend/inventory/ent/gen/predicate"
	entrepositorymovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repositorymovement"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
	enttransaction "github.com/pyck-ai/pyck/backend/inventory/ent/gen/transaction"
)

// service is the package-private concrete implementation of Service. Its
// shape matches the historical *services.InventoryStockService — only the
// type name and visibility have changed in Step 2.9.2.
type service struct {
	// debugLog, when non-nil, receives structured trace lines for every stock
	// mutation (both real-ops and rebuild paths). Used exclusively by tests
	// to compare the two execution paths.
	debugLog io.Writer

	// dbDialect is the configured Ent dialect (e.g. dialect.Postgres or
	// dialect.SQLite). Step 7.2 uses it to gate the
	// inventory.create_item_movement_proc call: only Postgres backends ship
	// the proc (it is a hand-written PL/pgSQL migration), so the SQLite
	// in-package tests fall back to the legacy Go orchestration. Empty
	// string means "no proc dispatch" — the Go path runs unconditionally.
	dbDialect string

	// outboxEmitter, when non-nil, is invoked after createItemMovementViaProc
	// to publish the outbox event that the bypassed Ent hook would have
	// written. Without it, mutations on the proc fast path would never reach
	// NATS / signal-router and any GraphQL workflow_reply waiter on the
	// resolver side would block until OutboxReplyTimeout. Typically wired to
	// events.EventSystem.EmitEvent at construction.
	outboxEmitter OutboxEmitter
}

// SetDebugLog installs (or clears) the trace writer.
func (s *service) SetDebugLog(w io.Writer) {
	s.debugLog = w
}

// debugf writes a formatted line to debugLog when it is set.
func (s *service) debugf(format string, args ...any) {
	if s.debugLog != nil {
		fmt.Fprintf(s.debugLog, format+"\n", args...)
	}
}

func (e *service) Close() {
}

// calculateRepositoryStockMap is a single-endpoint executor walk. Callers
// that know both endpoints (always invoked in FROM/TO pairs in practice)
// should compute the lcaID once via lowestCommonAncestor against the same
// repoMap passed in here, and thread it through both calls so the walk
// stops at the LCA above the endpoints instead of climbing to the tree
// root. Pass uuid.Nil when no LCA is available (e.g. disjoint trees) to
// preserve the historical walk-to-root behavior.
//
// repoMap and priorStocks are required (non-nil) and must already include
// every repository on the parent chain from repositoryID up to the tree
// root (or the LCA). The function performs zero database reads — Step 4.3
// moved the load up into the caller via loadAncestorStocks.
func (s *service) calculateRepositoryStockMap(itemID, repositoryID, lcaID uuid.UUID, quantity int64, repoMap map[uuid.UUID]ent.Repository, priorStocks map[stockKey]ent.Stock, stockMap map[uuid.UUID]ent.Stock, ownStock bool) error {
	if err := s.applyRepositoryStockDelta(itemID, repositoryID, lcaID, quantity, repoMap, priorStocks, stockMap, ownStock); err != nil {
		return err
	}
	return s.validateStockMapNoUnderflow(stockMap)
}

// applyItemMovementStockDelta applies the FROM-walk and TO-walk for an item
// movement and validates the resulting stockMap once both walks have run. This
// is the correct shape for the executor: validating per-walk over-rejects the
// case where FROM and TO share a common ancestor whose net delta is zero.
//
// The LCA of FROM and TO is computed once at entry from the supplied repoMap
// and threaded into both walks so each step is O(1). Above the LCA the +q
// from the TO-walk and the -q from the FROM-walk cancel exactly (FINDINGS
// section 3.4), so the walks terminate at the LCA itself rather than
// climbing to the tree root. When lowestCommonAncestor returns uuid.Nil
// (e.g. virtual vs non-virtual trees with no shared ancestor), the LCA
// cutoff never fires and each walk continues to its own root, preserving
// the historical behavior.
//
// repoMap and priorStocks are required (non-nil): they hold the
// pre-loaded ancestor closure produced by loadAncestorStocks. The
// function does not load anything itself and performs zero database
// reads. Callers that lack the maps must call loadAncestorStocks
// explicitly — there is no nil-fallback codepath (see TODO.md
// Step 4.3 / FINDINGS §3.5).
func (s *service) applyItemMovementStockDelta(itemID, fromID, toID uuid.UUID, quantity int64, repoMap map[uuid.UUID]ent.Repository, priorStocks map[stockKey]ent.Stock, stockMap map[uuid.UUID]ent.Stock, ownStock bool) error {
	lcaID := lowestCommonAncestor(repoMap, fromID, toID)
	if err := s.applyRepositoryStockDelta(itemID, fromID, lcaID, -quantity, repoMap, priorStocks, stockMap, ownStock); err != nil {
		return err
	}
	if err := s.applyRepositoryStockDelta(itemID, toID, lcaID, quantity, repoMap, priorStocks, stockMap, ownStock); err != nil {
		return err
	}
	return s.validateStockMapNoUnderflow(stockMap)
}

// validateStockMapNoUnderflow returns errStockUnderflow if any non-virtual
// repository in stockMap has a negative Quantity. Virtual repos are clamped
// to zero by applyRepositoryStockDelta, so a single Quantity < 0 check is
// sufficient.
func (s *service) validateStockMapNoUnderflow(stockMap map[uuid.UUID]ent.Stock) error {
	for repoID, rec := range stockMap {
		if rec.Quantity < 0 {
			return fmt.Errorf("%w: repository=%s quantity would be %d", errStockUnderflow, repoID, rec.Quantity)
		}
	}
	return nil
}

// applyRepositoryStockDelta walks the parent chain of repositoryID and
// accumulates the delta into stockMap without validating underflow. Callers
// must run ValidateStockMapNoUnderflow after all desired walks have been
// applied; transient negative quantities at intermediate ancestors are
// expected when FROM and TO share a common ancestor.
//
// The walk terminates at lcaID once the LCA itself has been processed —
// above the LCA the +q and -q deltas cancel exactly so visiting any further
// ancestor would only emit no-op snapshot rows (FINDINGS section 3.4). When
// lcaID is uuid.Nil (no shared ancestor or LCA lookup deferred), the walk
// continues to a real tree root, preserving the historical behavior.
//
// repoMap is required (non-nil) and must contain every repository on the
// chain from repositoryID up to (and including) the stop node. priorStocks
// is required (non-nil) and is keyed by (repository, item); it carries the
// pre-loaded latest stock row per pair, populated up-front by the caller
// via loadAncestorStocks. The function performs zero database reads — see
// TODO.md Step 4.3 / FINDINGS §3.5. There is intentionally no
// nil-fallback codepath: a missing repo is treated as a hard error so that
// every silently-slow path is closed.
func (s *service) applyRepositoryStockDelta(itemID, repositoryID, lcaID uuid.UUID, quantity int64, repoMap map[uuid.UUID]ent.Repository, priorStocks map[stockKey]ent.Stock, stockMap map[uuid.UUID]ent.Stock, ownStock bool) error {
	stockRecordQuantity := int64(0)
	incomingStockRecordQuantity := int64(0)
	outgoingStockRecordQuantity := int64(0)

	ownStockRecordQuantity := int64(0)
	ownIncomingStockRecordQuantity := int64(0)
	ownOutgoingStockRecordQuantity := int64(0)

	// Retrieve current values for repository from the pre-loaded map. The
	// map is built by loadAncestorStocks at the top of every executor
	// caller; if a repo is missing the caller's seed list was incomplete
	// and we want a hard, loud failure rather than a silent DB fallback.
	repo, ok := repoMap[repositoryID]
	if !ok {
		return fmt.Errorf("%w: %s", errAncestorRepoNotPreloaded, repositoryID)
	}

	var stockRecord *ent.Stock
	priorStockRecord, priorFound := priorStocks[stockKey{RepositoryID: repositoryID, ItemID: itemID}]
	if priorFound {
		stockRecord = &priorStockRecord
	}

	// Check is stockMap already contains repositoryID; if not, fall back to
	// the pre-loaded baseline (priorStocks) — no DB read.
	if record, found := stockMap[repositoryID]; found {
		stockRecordQuantity = record.Quantity
		incomingStockRecordQuantity = record.IncomingStock
		outgoingStockRecordQuantity = record.OutgoingStock
		ownStockRecordQuantity = record.OwnQuantity
		ownIncomingStockRecordQuantity = record.OwnIncomingStock
		ownOutgoingStockRecordQuantity = record.OwnOutgoingStock
	} else if stockRecord != nil {
		stockRecordQuantity = stockRecord.Quantity
		incomingStockRecordQuantity = stockRecord.IncomingStock
		outgoingStockRecordQuantity = stockRecord.OutgoingStock
		ownStockRecordQuantity = stockRecord.OwnQuantity
		ownIncomingStockRecordQuantity = stockRecord.OwnIncomingStock
		ownOutgoingStockRecordQuantity = stockRecord.OwnOutgoingStock
	}

	// Calculate the new stock
	stockRecordQuantity = stockRecordQuantity + quantity
	if repo.VirtualRepo {
		// Virtual repositories are infinite sources/sinks — clamp to zero
		stockRecordQuantity = 0
	}

	var updatedRecord ent.Stock
	if rec, found := stockMap[repositoryID]; found {
		updatedRecord = rec
	} else if stockRecord != nil {
		updatedRecord = *stockRecord
	}

	updatedRecord.Quantity = stockRecordQuantity
	if quantity > 0 {
		updatedRecord.IncomingStock = std.Max(incomingStockRecordQuantity-quantity, 0)
	} else {
		updatedRecord.OutgoingStock = std.Max(outgoingStockRecordQuantity+quantity, 0)
	}

	if ownStock {
		ownStockRecordQuantity = std.Max(ownStockRecordQuantity+quantity, 0)
		if repo.VirtualRepo {
			ownStockRecordQuantity = 0
		}
		updatedRecord.OwnQuantity = ownStockRecordQuantity
		if quantity > 0 {
			updatedRecord.OwnIncomingStock = std.Max(ownIncomingStockRecordQuantity-quantity, 0)
		} else {
			updatedRecord.OwnOutgoingStock = std.Max(ownOutgoingStockRecordQuantity+quantity, 0)
		}
	}

	stockMap[repositoryID] = updatedRecord

	s.debugf("REALEXEC repo=%s item=%s Qty=%d Own=%d In=%d Out=%d OwnIn=%d OwnOut=%d delta=%d own=%v",
		repositoryID, itemID, updatedRecord.Quantity, updatedRecord.OwnQuantity,
		updatedRecord.IncomingStock, updatedRecord.OutgoingStock,
		updatedRecord.OwnIncomingStock, updatedRecord.OwnOutgoingStock,
		quantity, ownStock)

	// Stop at the LCA: above it, the +q from the TO-walk and the -q from the
	// FROM-walk cancel exactly, so visiting any further ancestor would only
	// emit no-op snapshot rows. When lcaID is uuid.Nil (no shared ancestor,
	// e.g. virtual vs non-virtual roots, or LCA lookup deferred by the
	// caller), this check never fires and the walk continues to a real tree
	// root, preserving the historical behavior.
	if repositoryID == lcaID || repo.ParentID == uuid.Nil {
		return nil
	}

	return s.applyRepositoryStockDelta(itemID, repo.ParentID, lcaID, quantity, repoMap, priorStocks, stockMap, false)
}

func (s *service) simulateRepositoryStockMap(itemID, repositoryID, sourceRepositoryID uuid.UUID, quantity int64, stockMap map[uuid.UUID]map[uuid.UUID]ent.Stock, repoMap map[uuid.UUID]ent.Repository, ownStock bool) error {
	return s.simulateRepositoryStockMapWithMode(itemID, repositoryID, sourceRepositoryID, quantity, stockMap, repoMap, ownStock, false)
}

// simulateRepositoryStockMapWithMode recurses up the ancestor chain of
// repositoryID applying quantity to each repo's stockMap entry. Above the LCA
// of repositoryID and sourceRepositoryID, the FROM-decrement and the
// TO-increment cancel exactly (see FINDINGS section 3.4), so we cap the walk
// at the LCA: the LCA itself is processed (its entry is updated by both the
// FROM-walk and the TO-walk, netting to zero), but recursion does not
// propagate to its parent.
//
// When sourceRepositoryID and repositoryID share no common ancestor (e.g.,
// virtual vs non-virtual trees), lowestCommonAncestor returns uuid.Nil; the
// equality check below never fires and the walk continues to a real tree
// root, preserving the historical behavior. The lcaID is computed once at
// the outer entry and threaded through the recursion to keep each step O(1).
func (s *service) simulateRepositoryStockMapWithMode(itemID, repositoryID, sourceRepositoryID uuid.UUID, quantity int64, stockMap map[uuid.UUID]map[uuid.UUID]ent.Stock, repoMap map[uuid.UUID]ent.Repository, ownStock bool, subtract bool) error {
	lcaID := lowestCommonAncestor(repoMap, repositoryID, sourceRepositoryID)
	return s.simulateRepositoryStockMapWalk(itemID, repositoryID, sourceRepositoryID, lcaID, quantity, stockMap, repoMap, ownStock, subtract)
}

// simulateRepositoryStockMapWalk is the recursive worker. It carries the
// pre-computed lcaID (uuid.Nil when there is no shared ancestor) so each
// recursion step is O(1) rather than recomputing the LCA. Stop conditions:
// (1) we have just processed the LCA itself, do not propagate above it;
// (2) we have reached a tree root (ParentID == uuid.Nil).
func (s *service) simulateRepositoryStockMapWalk(itemID, repositoryID, sourceRepositoryID, lcaID uuid.UUID, quantity int64, stockMap map[uuid.UUID]map[uuid.UUID]ent.Stock, repoMap map[uuid.UUID]ent.Repository, ownStock bool, subtract bool) error {
	if stockMap[repositoryID] == nil {
		stockMap[repositoryID] = make(map[uuid.UUID]ent.Stock)
	}

	updatedRecord := stockMap[repositoryID][itemID]

	if ownStock { //nolint:nestif // verbatim move from services/inventory_stock.go; structural simplification is out of scope for Step 2.9.2
		if quantity > 0 {
			if subtract {
				// Subtracting mode: positive quantity reduces incoming stock
				updatedRecord.OwnIncomingStock = std.Max(stockMap[repositoryID][itemID].OwnIncomingStock-quantity, 0)
			} else {
				// Adding mode: positive quantity increases incoming stock
				updatedRecord.OwnIncomingStock = std.Max(stockMap[repositoryID][itemID].OwnIncomingStock+quantity, 0)
			}
		} else {
			if subtract {
				// Subtracting mode: negative quantity reduces outgoing stock
				updatedRecord.OwnOutgoingStock = std.Max(stockMap[repositoryID][itemID].OwnOutgoingStock+quantity, 0)
			} else {
				// Adding mode: negative quantity increases outgoing stock
				updatedRecord.OwnOutgoingStock = std.Max(stockMap[repositoryID][itemID].OwnOutgoingStock-quantity, 0)
			}
		}
	}

	// Avoid tracking the internal movements within repository. Steps 3.2/3.3
	// stop the simulate and executor walks at the LCA, so when this branch
	// runs we only need to suppress the non-own update at the LCA itself —
	// every other visited node is a strict ancestor of the LCA, where the
	// move is *not* internal. Below the LCA the walks never reach this
	// function, so the lcaID-only check is exhaustive and the historical
	// recursive child-of-check is redundant.
	if repositoryID != lcaID { //nolint:nestif // verbatim move from services/inventory_stock.go; structural simplification is out of scope for Step 2.9.2
		if quantity > 0 {
			if subtract {
				// Subtracting mode: positive quantity reduces incoming stock
				updatedRecord.IncomingStock = std.Max(stockMap[repositoryID][itemID].IncomingStock-quantity, 0)
			} else {
				// Adding mode: positive quantity increases incoming stock
				updatedRecord.IncomingStock = std.Max(stockMap[repositoryID][itemID].IncomingStock+quantity, 0)
			}
		} else {
			if subtract {
				// Subtracting mode: negative quantity reduces outgoing stock
				updatedRecord.OutgoingStock = std.Max(stockMap[repositoryID][itemID].OutgoingStock+quantity, 0)
			} else {
				// Adding mode: negative quantity increases outgoing stock
				updatedRecord.OutgoingStock = std.Max(stockMap[repositoryID][itemID].OutgoingStock-quantity, 0)
			}
		}
	}
	stockMap[repositoryID][itemID] = updatedRecord

	mode := "CREATE"
	if subtract {
		mode = "DELETE"
	}
	s.debugf("%s repo=%s item=%s In=%d Out=%d OwnIn=%d OwnOut=%d delta=%d own=%v src=%s",
		mode, repositoryID, itemID,
		updatedRecord.IncomingStock, updatedRecord.OutgoingStock,
		updatedRecord.OwnIncomingStock, updatedRecord.OwnOutgoingStock,
		quantity, ownStock, sourceRepositoryID)

	// Stop at the LCA: above it, the +q from the TO-walk and the -q from
	// the FROM-walk cancel exactly, so visiting any further ancestor would
	// only emit no-op snapshot rows. When lcaID is uuid.Nil (no shared
	// ancestor, e.g., virtual vs non-virtual roots), this check never
	// fires and the walk continues to a real tree root, preserving the
	// historical behavior.
	if repositoryID == lcaID || repoMap[repositoryID].ParentID == uuid.Nil {
		return nil
	}

	return s.simulateRepositoryStockMapWalk(itemID, repoMap[repositoryID].ParentID, sourceRepositoryID, lcaID, quantity, stockMap, repoMap, false, subtract)
}

// GetCurrentRepositoriesStock is used by RebuildStockTable / replayRebuildEvent
// and the legacy delete-* resolvers (DeleteInventoryItem,
// DeleteInventoryRepository, DeleteInventoryStock); do not call from hot paths.
// Direct mutation paths load their stock baselines through loadAncestorStocks
// instead.
func (s *service) GetCurrentRepositoriesStock(ctx context.Context, tx *ent.Tx, repositoryIDs []uuid.UUID) (map[uuid.UUID]map[uuid.UUID]ent.Stock, error) {
	repoPred := entstock.RepositoryIDIn(repositoryIDs...)

	records, err := tx.Stock.Query().
		Where(repoPred).
		DistinctOnExists(
			[]string{entstock.FieldRepositoryID, entstock.FieldItemID},
			entstock.FieldCreatedAt,
			repoPred,
		).
		AllPages(ctx, mixin.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query stock table: %w", err)
	}

	stockMap := make(map[uuid.UUID]map[uuid.UUID]ent.Stock)
	for _, r := range records {
		if _, ok := stockMap[r.RepositoryID]; !ok {
			stockMap[r.RepositoryID] = make(map[uuid.UUID]ent.Stock)
		}
		stockMap[r.RepositoryID][r.ItemID] = *r
	}

	return stockMap, nil
}

// GetRepositoriesDetails is used by RebuildStockTable / replayRebuildEvent and
// the legacy delete-* resolvers (DeleteInventoryItem, DeleteInventoryRepository);
// do not call from hot paths. Direct mutation paths load their repository
// ancestors through loadAncestorStocks instead.
func (s *service) GetRepositoriesDetails(ctx context.Context, tx *ent.Tx) (map[uuid.UUID]ent.Repository, error) {
	repos, err := tx.Repository.Query().AllPages(ctx, mixin.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed reading repositories details: %w", err)
	}

	result := make(map[uuid.UUID]ent.Repository, len(repos))
	for _, v := range repos {
		result[v.ID] = *v
	}

	return result, nil
}

func (s *service) getRepositoriesParentsDetails(repositoryID uuid.UUID, reposMap, parentsMap map[uuid.UUID]ent.Repository) error {
	for _, v := range reposMap {
		if v.ID == repositoryID {
			parentsMap[repositoryID] = reposMap[repositoryID]

			if reposMap[repositoryID].ParentID == uuid.Nil {
				return nil
			}
			return s.getRepositoriesParentsDetails(v.ParentID, reposMap, parentsMap)
		}
	}

	return nil
}

func (s *service) checkExecuteNextMovementByPosition(ctx context.Context, tx *ent.Tx, collectionID uuid.UUID, position int) error {
	// Probe the (collection_id, position) composite indexes added in
	// Phases 1.2 / 1.4: an Exist() bounded by PositionLT(position) is an
	// index range probe, not a full collection scan.
	itemPending, err := tx.ItemMovement.Query().
		Where(
			entitemmovement.CollectionID(collectionID),
			entitemmovement.PositionLT(position),
			entitemmovement.DeletedAtIsNil(),
			entitemmovement.Executed(false),
		).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("failed to query item movements: %w", err)
	}
	if itemPending {
		return errOutOfOrderExecution
	}

	repoPending, err := tx.RepositoryMovement.Query().
		Where(
			entrepositorymovement.CollectionID(collectionID),
			entrepositorymovement.PositionLT(position),
			entrepositorymovement.DeletedAtIsNil(),
			entrepositorymovement.Executed(false),
		).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("failed to query repository movements: %w", err)
	}
	if repoPending {
		return errOutOfOrderExecution
	}

	return nil
}

// CreateItemMovement orchestrates the create path for a non-collection
// item movement.
//
// On Postgres (production) the entire flow — virtual-repo guard, stock
// availability check, FROM/TO ancestor walk, LCA trim, per-ancestor stock
// snapshot insert, and per-(tenant, repo, item) version increment — runs
// inside the inventory.create_item_movement_proc PL/pgSQL function added
// in Step 7.1. The proc replaces what was previously 4 reads + N writes
// (N = FROM/TO ancestor walk depth) with a single round trip plus a
// server-side LOOP/EXCEPTION retry budget tuned to the Phase 6.1 unique
// index. See FINDINGS §2C ("server-side retry") and §4 step 8.
//
// On non-Postgres dialects (the SQLite in-package tests do not ship the
// proc — it lives in a hand-written PL/pgSQL migration) the legacy Go
// orchestration runs verbatim. The behavioral contract is identical
// between the two paths; the proc body documents the equivalence
// statement-by-statement against this function (see
// 20260430070249_create_item_movement_proc.up.sql).
//
// Step 5.1 (FINDINGS §3.11): the resolver-supplied uniqueness hook runs
// BEFORE the movement INSERT (and BEFORE the proc dispatch on Postgres),
// not after. Failing fast keeps a duplicate-input request to a single
// cheap COUNT(*) per unique field instead of writing the movement row
// plus the entire stockMap snapshot before the validator's COUNT(*)
// returned a conflict.
func (s *service) CreateItemMovement(ctx context.Context, tx *ent.Tx, dto CreateItemMovementInput) (*ent.ItemMovement, error) {
	// Phase 9.2: when the caller has opted into deferred underflow,
	// always run the Go body. The plpgsql proc raises STOCK_INSUFFICIENT
	// internally and adding a "skip check" parameter would couple it to
	// the collection plan-ahead semantics; keeping the proc focused on
	// the single-movement fast path and routing collection-tier callers
	// through the Go body is the cheaper invariant. The Go path
	// consults IsDeferredUnderflow at its FROM-availability gate so the
	// bookkeeping (movement INSERT, stock-map fan-out) still runs but
	// the per-call error is suppressed.
	if s.dbDialect == dialect.Postgres && !IsDeferredUnderflow(ctx) {
		return s.createItemMovementViaProc(ctx, tx, dto)
	}
	return s.createItemMovementViaGo(ctx, tx, dto)
}

// createItemMovementViaGo is the legacy Go orchestration body. It is the
// fallback path for non-Postgres dialects (the SQLite tests in this
// package). The Postgres path goes through createItemMovementViaProc /
// inventory.create_item_movement_proc instead. Behavior is preserved
// verbatim from the pre-Step-7.2 implementation so the SQLite tests pin
// the exact contract the proc implements server-side.
func (s *service) createItemMovementViaGo(ctx context.Context, tx *ent.Tx, dto CreateItemMovementInput) (*ent.ItemMovement, error) {
	input := dto.Input

	repo, err := tx.Repository.Get(ctx, input.FromID)
	if err != nil {
		return nil, err
	}

	if repo.VirtualRepo { //nolint:nestif // verbatim move from CreateInventoryItemMovement resolver in Step 2.9.3a; structural simplification is out of scope.
		toRepo, terr := tx.Repository.Get(ctx, input.ToID)
		if terr != nil {
			return nil, terr
		}
		if toRepo.VirtualRepo {
			return nil, errVirtualRepoMovement
		}
	} else {
		where := entpredicate.Stock(func(sel *sql.Selector) {
			sel.Where(sql.EQ(entstock.RepositoryColumn, input.FromID))
			sel.Where(sql.EQ(entstock.ItemColumn, input.ItemID))
		})
		stockRecord, qerr := tx.Stock.Query().
			Where(where).
			Where(entstock.TenantID(dto.TenantID)).
			Order(ent.Desc(entstock.FieldCreatedAt)).
			First(ctx)
		// "stock not found" err means that stock for item-repository combination is 0
		if qerr != nil && !ent.IsNotFound(qerr) {
			return nil, fmt.Errorf("failed reading stock: %w", qerr)
		}
		// Phase 9.2: collection-tier callers wrap ctx with
		// WithDeferredUnderflow so that a chain like A->B->C (where B
		// only has stock after A->B has been written) does not fail on
		// the second movement before its predecessor's stock-map
		// fan-out has run. The bookkeeping below (movement INSERT,
		// incoming/outgoing/quantity updates) still runs; only the
		// per-call rejection is suppressed. The caller is responsible
		// for invoking consistencyCheckSourceRows on the union of
		// touched (repo, item) pairs after its loop completes.
		if stockRecord == nil || stockRecord.Quantity+stockRecord.IncomingStock-stockRecord.OutgoingStock < input.Quantity {
			if !IsDeferredUnderflow(ctx) {
				return nil, errInsufficientStock
			}
		}
	}

	// Phase 4.2: replace the unbounded GetRepositoriesDetails +
	// GetCurrentRepositoriesStock pair with a single recursive-CTE
	// ancestor load seeded by [FromID, ToID] and narrowed to the moved
	// item. The simulate walks only ever read parents of FROM and TO,
	// so loading the entire tenant's repository tree was wasted work
	// once the LCA cutoff landed in Phase 3 (FINDINGS sections 3.2 and
	// 3.5). The flat map[stockKey]Stock is nested back into the legacy
	// shape via nestStockKeyMap so the simulate / insertStockMap
	// helpers stay untouched here; Phase 4.3 will switch them to the
	// flat shape and drop the adapter.
	repositoryMap, ancestorStocks, err := s.loadAncestorStocks(ctx, tx, dto.TenantID, []uuid.UUID{input.FromID, input.ToID}, []uuid.UUID{input.ItemID}, false)
	if err != nil {
		return nil, err
	}
	stockMap := nestStockKeyMap(ancestorStocks)

	// Calculate stockMap for parents of the 'from' repository
	if err = s.simulateRepositoryStockMap(input.ItemID, input.FromID, input.ToID, -1*input.Quantity, stockMap, repositoryMap, true); err != nil {
		return nil, fmt.Errorf("failed calculating stock map: %w", err)
	}

	// Calculate stockMap for parents of the 'to' repository
	if err = s.simulateRepositoryStockMap(input.ItemID, input.ToID, input.FromID, input.Quantity, stockMap, repositoryMap, true); err != nil {
		return nil, fmt.Errorf("failed calculating stock map: %w", err)
	}

	if dto.ValidateUniquenessHook != nil {
		if err = dto.ValidateUniquenessHook(); err != nil {
			return nil, err
		}
	}

	movement, err := tx.ItemMovement.
		Create().
		SetInput(input).
		SetExecuted(false).
		Save(ctx)
	if err != nil {
		return nil, err
	}

	if err = s.insertStockMap(ctx, tx, input.ItemID, dto.TenantID, movement.ID, stockMap); err != nil {
		return nil, err
	}

	return movement, nil
}

// createItemMovementViaProc is the Postgres-only fast path that delegates
// the entire create flow to inventory.create_item_movement_proc. The proc
// (see 20260430070249_create_item_movement_proc.up.sql) handles the
// virtual-repo guard, stock availability check, FROM/TO ancestor closure,
// LCA trim, per-ancestor delta application, movement INSERT, and stock
// fan-out — all within a single round trip and a server-side LOOP retry
// against the per-(tenant, repo, item) unique version index.
//
// The Go side is responsible for:
//
//   - Running ValidateUniquenessHook BEFORE the proc call (Step 5.1
//     fast-fail; the proc's idempotency does not extend to data-uniqueness
//     conflicts because those are resolved against the resolver-validated
//     DataType, not the bare jsonb payload).
//   - Pre-generating a v7 movement ID so the response shape is available
//     without an extra round trip.
//   - Mapping proc-raised RAISE EXCEPTION strings back to the same
//     sentinels the Go path returns (errVirtualRepoMovement,
//     errInsufficientStock). The proc message wording is pinned in the
//     migration so a string-prefix match here is deliberate.
//   - Wrapping any 23505 the proc surfaces after exhausting its retry
//     budget so wrapOCCConflict / gqltx middleware can retry the whole
//     request the same way as the Go path.
//   - Reloading the *ent.ItemMovement after the proc returns so the
//     resolver response shape is identical to the Go path.
func (s *service) createItemMovementViaProc(ctx context.Context, tx *ent.Tx, dto CreateItemMovementInput) (*ent.ItemMovement, error) {
	input := dto.Input

	if dto.ValidateUniquenessHook != nil {
		if err := dto.ValidateUniquenessHook(); err != nil {
			return nil, err
		}
	}

	movementID := uuidgql.GenerateV7UUID()
	createdBy := authn.ForContext(ctx).ID

	dataJSON := []byte("{}")
	if input.Data != nil {
		raw, err := json.Marshal(input.Data)
		if err != nil {
			return nil, fmt.Errorf("create_item_movement_proc: marshal data: %w", err)
		}
		dataJSON = raw
	}

	collectionID := uuid.Nil
	if input.CollectionID != nil {
		collectionID = *input.CollectionID
	}
	var orderID any
	if input.OrderID != nil {
		orderID = *input.OrderID
	}
	var position any
	if input.Position != nil {
		position = *input.Position
	}
	var dataTypeID any
	if input.DataTypeID != nil {
		dataTypeID = *input.DataTypeID
	}
	var dataTypeSlug any
	if input.DataTypeSlug != nil {
		dataTypeSlug = *input.DataTypeSlug
	}

	const procCall = `SELECT inventory.create_item_movement_proc(` +
		`$1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::bigint, $6::text, ` +
		`$7::uuid, $8::uuid, $9::int, $10::uuid, $11::text, $12::jsonb, ` +
		`$13::uuid, $14::uuid)`

	var returnedID uuid.UUID
	rows, err := tx.QueryContext(ctx, procCall,
		dto.TenantID,
		input.ItemID,
		input.FromID,
		input.ToID,
		input.Quantity,
		input.Handler,
		collectionID,
		orderID,
		position,
		dataTypeID,
		dataTypeSlug,
		dataJSON,
		createdBy,
		movementID,
	)
	if err != nil {
		return nil, mapCreateItemMovementProcError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if rerr := rows.Err(); rerr != nil {
			return nil, mapCreateItemMovementProcError(rerr)
		}
		return nil, errCreateItemMovementProcNoRows
	}
	if err := rows.Scan(&returnedID); err != nil {
		return nil, fmt.Errorf("create_item_movement_proc: scan movement id: %w", err)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, mapCreateItemMovementProcError(rerr)
	}
	// Close explicitly so the subsequent Ent query reuses the connection.
	if cerr := rows.Close(); cerr != nil {
		return nil, fmt.Errorf("create_item_movement_proc: close rows: %w", cerr)
	}

	movement, err := tx.ItemMovement.Get(ctx, returnedID)
	if err != nil {
		return nil, fmt.Errorf("create_item_movement_proc: reload movement: %w", err)
	}

	// The proc INSERTed the movement directly via raw SQL, bypassing the
	// Ent mutation hook. Emit the outbox event manually so downstream
	// consumers (signal-router, workflow_reply waiters) still observe this
	// create. The emit runs in the same tx as the proc, so the outbox row
	// commits atomically with the movement row. A nil emitter is tolerated
	// for the SQLite test paths where the event system is not wired.
	if s.outboxEmitter != nil {
		if err := s.outboxEmitter(ctx, "ItemMovement", "create", returnedID, movement, nil); err != nil {
			return nil, fmt.Errorf("create_item_movement_proc: emit outbox event: %w", err)
		}
	}

	return movement, nil
}

// mapCreateItemMovementProcError converts the create_item_movement_proc
// RAISE EXCEPTION strings back into the same Go sentinels the legacy
// Go path returns, and wraps any unique-violation on the stock OCC index
// into errOCCConflict so gqltx's retry middleware treats the proc's
// budget-exhausted reraise the same as a Go-side conflict.
//
// The proc message wording is pinned in the migration; see
// 20260430070249_create_item_movement_proc.up.sql for the exact strings.
func mapCreateItemMovementProcError(err error) error {
	if err == nil {
		return nil
	}
	if msg := err.Error(); msg != "" {
		switch {
		case strings.Contains(msg, "movements between virtual repositories are not allowed"):
			return errVirtualRepoMovement
		case strings.Contains(msg, "STOCK_INSUFFICIENT"):
			return errInsufficientStock
		}
	}
	return wrapOCCConflict(err)
}

// ExecuteItemMovement is the orchestration body that previously lived in the
// ExecuteInventoryItemMovement resolver. The flow is verbatim: load the
// current movement, reject already-executed movements, enforce
// position-ordered execution within the collection, flip the executed
// flag, write the OUT/INTO transaction pair, apply the FROM/TO parent-walk
// stock delta in a single pass (so net-zero deltas through a common ancestor
// do not raise a spurious underflow — see #1159), then fan out the stock
// rows tagged with the movement ID.
func (s *service) ExecuteItemMovement(ctx context.Context, tx *ent.Tx, dto ExecuteItemMovementInput) (*ent.ItemMovement, error) {
	id := dto.ID

	// Retrieve current values for item movement
	currentMovementValues, err := tx.ItemMovement.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("itemMovement not found") //nolint:err113 // verbatim move from ExecuteInventoryItemMovement resolver in Step 2.9.3b; tightening errors is out of scope.
	}

	// Executed mutation cannot be executed again
	if currentMovementValues.Executed {
		return nil, fmt.Errorf("ItemMovement is already executed") //nolint:err113 // verbatim move from ExecuteInventoryItemMovement resolver in Step 2.9.3b; tightening errors is out of scope.
	}

	// Check position
	if err = s.checkExecuteNextMovementByPosition(ctx, tx, currentMovementValues.CollectionID, currentMovementValues.Position); err != nil {
		return nil, err
	}

	// Update 'executed' flag
	itemMovement, err := tx.ItemMovement.
		UpdateOneID(id).
		SetExecuted(true).
		SetExecutedAt(time.Now().UTC()).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed updating itemMovement: %w", err)
	}

	// Add out transaction

	outTransaction := tx.Transaction.
		Create().
		SetItemID(itemMovement.ItemID).
		SetTenantID(dto.TenantID).
		SetRepositoryID(itemMovement.FromID).
		SetType(enttransaction.TypeOut).
		SetQuantity(itemMovement.Quantity).
		SetCreatedBy(itemMovement.UpdatedBy)

	intoTransaction := tx.Transaction.
		Create().
		SetItemID(itemMovement.ItemID).
		SetTenantID(dto.TenantID).
		SetRepositoryID(itemMovement.ToID).
		SetType(enttransaction.TypeInto).
		SetQuantity(itemMovement.Quantity).
		SetCreatedBy(itemMovement.UpdatedBy)

	if _, err = tx.Transaction.CreateBulk(outTransaction, intoTransaction).Save(ctx); err != nil {
		return nil, fmt.Errorf("failed inserting transactions: %w", err)
	}

	// Create stock map. This map will contain all future Stock inserts
	stockMap := make(map[uuid.UUID]ent.Stock, 0)

	// Phase 4.3: applyItemMovementStockDelta now requires a pre-loaded
	// repoMap and priorStocks map. Seed the loader with [FromID, ToID]
	// and narrow to the moving item — that is exactly the closure the
	// FROM-walk and TO-walk traverse. See FINDINGS §3.5.
	repoMap, priorStocks, err := s.loadAncestorStocks(ctx, tx, dto.TenantID, []uuid.UUID{currentMovementValues.FromID, currentMovementValues.ToID}, []uuid.UUID{currentMovementValues.ItemID}, false)
	if err != nil {
		return nil, fmt.Errorf("failed loading ancestor stocks: %w", err)
	}

	// Apply both FROM and TO walks in a single pass; validation is deferred so
	// that net-zero deltas through a common ancestor of FROM and TO do not
	// raise a spurious underflow (#1159).
	if err = s.applyItemMovementStockDelta(currentMovementValues.ItemID, currentMovementValues.FromID, currentMovementValues.ToID, itemMovement.Quantity, repoMap, priorStocks, stockMap, true); err != nil {
		return nil, fmt.Errorf("failed calculating stock map: %w", err)
	}

	// Insert new stocks. The version column (Phase 6.1) is a per-(tenant,
	// repo, item) monotonic counter; seed the tracker from the latest row
	// per group so each new INSERT lands at latest+1.
	versions := newStockVersionTracker(priorStocks)
	stocksToCreate := []*ent.StockCreate{}
	for repoID, repoRecord := range stockMap {
		newStock := tx.Stock.
			Create().
			SetItemID(itemMovement.ItemID).
			SetTenantID(dto.TenantID).
			SetRepositoryID(repoID).
			SetQuantity(repoRecord.Quantity).
			SetCreatedBy(itemMovement.UpdatedBy).
			SetIncomingStock(repoRecord.IncomingStock).
			SetOutgoingStock(repoRecord.OutgoingStock).
			SetOwnQuantity(repoRecord.OwnQuantity).
			SetOwnIncomingStock(repoRecord.OwnIncomingStock).
			SetOwnOutgoingStock(repoRecord.OwnOutgoingStock).
			SetMovementID(id).
			SetVersion(versions.nextFor(repoID, itemMovement.ItemID))
		stocksToCreate = append(stocksToCreate, newStock)
	}
	if _, err = tx.Stock.CreateBulk(stocksToCreate...).Save(ctx); err != nil {
		return nil, wrapOCCConflict(fmt.Errorf("failed inserting stocks: %w", err))
	}

	return itemMovement, nil
}

// DeleteItemMovement is the orchestration body that previously lived in the
// DeleteInventoryItemMovement resolver. The flow is verbatim: load the
// movement, reject already-executed movements with errDeleteExecutedMovement,
// walk parent maps to scope the reservation reversal, simulate the FROM/TO
// stock-map deltas in subtract mode (so the incoming/outgoing reservations
// recorded at create time are removed), fan out the resulting stock rows
// tagged with the movement ID, then soft-delete the movement.
func (s *service) DeleteItemMovement(ctx context.Context, tx *ent.Tx, dto DeleteItemMovementInput) (*ent.ItemMovement, error) {
	id := dto.ID

	// Retrieve current values for item movement
	movement, err := tx.ItemMovement.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("itemMovement not found") //nolint:err113 // verbatim move from DeleteInventoryItemMovement resolver in Step 2.9.3c; tightening errors is out of scope.
	}

	// Cannot delete executed movements - they have already transferred actual stock
	if movement.Executed {
		return nil, errDeleteExecutedMovement
	}

	// Phase 4.5: replace the unbounded GetRepositoriesDetails +
	// GetCurrentRepositoriesStock pair with a single recursive-CTE
	// ancestor load seeded by [FromID, ToID] and narrowed to the moved
	// item. The simulate walks only ever read parents of FROM and TO,
	// so loading the entire tenant's repository tree was wasted work
	// once the LCA cutoff landed in Phase 3 (FINDINGS sections 3.2 and
	// 3.5). The flat map[stockKey]Stock is nested back into the legacy
	// shape via nestStockKeyMap so the simulate / insertStockMap
	// helpers stay untouched here.
	repositoryMap, ancestorStocks, err := s.loadAncestorStocks(ctx, tx, dto.TenantID, []uuid.UUID{movement.FromID, movement.ToID}, []uuid.UUID{movement.ItemID}, false)
	if err != nil {
		return nil, err
	}
	stockMap := nestStockKeyMap(ancestorStocks)

	// Reverse the stock reservations using subtract mode
	// Create used: FROM=-qty (adds outgoing), TO=+qty (adds incoming)
	// Delete uses: FROM=-qty with subtract (removes outgoing), TO=+qty with subtract (removes incoming)

	// FROM repository: remove outgoing stock using same sign as create but with subtract=true
	if err = s.simulateRepositoryStockMapWithMode(movement.ItemID, movement.FromID, movement.ToID, -1*movement.Quantity, stockMap, repositoryMap, true, true); err != nil {
		return nil, fmt.Errorf("failed calculating stock map: %w", err)
	}

	// TO repository: remove incoming stock using same sign as create but with subtract=true
	if err = s.simulateRepositoryStockMapWithMode(movement.ItemID, movement.ToID, movement.FromID, movement.Quantity, stockMap, repositoryMap, true, true); err != nil {
		return nil, fmt.Errorf("failed calculating stock map: %w", err)
	}

	// Insert stock records to clear reservations
	if err = s.insertStockMap(ctx, tx, movement.ItemID, dto.TenantID, id, stockMap); err != nil {
		return nil, err
	}

	deleted, err := tx.ItemMovement.UpdateOneID(id).
		SetDeletedAt(time.Now().UTC()).
		SetDeletedBy(dto.DeletedBy).
		Save(ctx)
	if err != nil {
		return nil, err
	}

	return deleted, nil
}

// CreateRepositoryMovement is the orchestration body that previously lived
// in the CreateInventoryRepositoryMovement resolver. The flow is: load the
// moving repository and its parent (used as the default FROM), reject
// virtual-to-virtual moves, resolve the effective FromID against the most
// recent non-executed movement for the same repository, walk parent maps
// for RepositoryID/ToID/FromID, simulate the FROM/TO stock-map deltas per
// item currently held by the moving repository, run the optional
// resolver-supplied uniqueness hook, persist the movement, then fan out
// the per-item stock rows tagged with the movement ID.
//
// Step 5.2 (FINDINGS §3.11): the uniqueness hook now runs BEFORE the
// movement INSERT. Previously a duplicate-input request would write the
// movement row plus the entire per-item stock-row fan-out before the
// validator's COUNT(*) returned a conflict, leaving the transaction abort
// to clean up what was effectively orphan write amplification. Failing
// fast keeps a duplicate to a single cheap COUNT(*) per unique field.
func (s *service) CreateRepositoryMovement(ctx context.Context, tx *ent.Tx, dto CreateRepositoryMovementInput) (*ent.RepositoryMovement, error) {
	input := dto.Input

	repo, err := tx.Repository.Get(ctx, input.RepositoryID)
	if err != nil {
		return nil, err
	}

	fromRepo, err := tx.Repository.Get(ctx, repo.ParentID)
	if err != nil {
		return nil, err
	}

	if fromRepo.VirtualRepo {
		toRepo, terr := tx.Repository.Get(ctx, input.ToID)
		if terr != nil {
			return nil, terr
		}

		if toRepo.VirtualRepo {
			return nil, errVirtualRepoMovement
		}
	}

	// Resolve the effective FromID before loading the ancestor closure so
	// that the stock fetch covers the correct hierarchy.
	lastRepoMovement, _ := tx.RepositoryMovement.Query().
		Where(
			entrepositorymovement.RepositoryID(input.RepositoryID),
			entrepositorymovement.Executed(false),
		).
		Order(ent.Desc(entrepositorymovement.FieldCreatedAt)).
		First(ctx)

	var expectedFromID uuid.UUID
	if lastRepoMovement != nil {
		expectedFromID = lastRepoMovement.ToID
	} else {
		expectedFromID = fromRepo.ID
	}

	if input.FromID != nil && *input.FromID != uuid.Nil {
		if *input.FromID != expectedFromID {
			return nil, fmt.Errorf("wrong FromID (%s) for repository (%s) movement. Expected FromID:%s", input.FromID, input.RepositoryID, expectedFromID) //nolint:err113 // verbatim move from CreateInventoryRepositoryMovement resolver in Step 2.9.3d; tightening errors is out of scope.
		}
	} else {
		input.FromID = &expectedFromID
	}

	// Phase 4.6: replace the unbounded GetRepositoriesDetails +
	// GetCurrentRepositoriesStock pair with a single recursive-CTE
	// ancestor load seeded by [RepositoryID, FromID, ToID]. The simulate
	// walks only ever read parents of FROM and TO, and the per-item
	// driver iterates current stock at the moving repository, so loading
	// the entire tenant's repository tree was wasted work once the LCA
	// cutoff landed in Phase 3 (FINDINGS sections 3.2 and 3.5). The flat
	// map[stockKey]Stock is nested back into the legacy shape via
	// nestStockKeyMap so the simulate / insertStockMap helpers stay
	// untouched here.
	//
	// Phase 4.7: narrow the closure stock read by the items physically
	// on the moving repository. Without this, loadAncestorStocks runs
	// with items=nil and hydrates every (ancestor_repo, item) pair in
	// the closure — on tenants whose ancestors have accumulated rich
	// unrelated stock history this can balloon into hundreds of pages
	// of SELECTs that the post-fan-out fix then throws away (only
	// items physically on the moving repo can change their rolled-up
	// stock; everything else is no-op write amplification at best
	// and a 65,535-parameter wire overflow at worst). Pre-querying the
	// moving repository's items is one focused SELECT against a single
	// repo_id — cheap relative to the closure load it shrinks.
	// DeleteRepositoryMovement already uses the same pattern at
	// impl.go:1278-1282 (originalQuantities keys -> items filter).
	movingRepoItemIDs, err := s.loadItemIDsAtRepo(ctx, tx, dto.TenantID, input.RepositoryID)
	if err != nil {
		return nil, err
	}
	repositoryMap, ancestorStocks, err := s.loadAncestorStocks(ctx, tx, dto.TenantID, []uuid.UUID{input.RepositoryID, *input.FromID, input.ToID}, movingRepoItemIDs, false)
	if err != nil {
		return nil, err
	}
	stockMap := nestStockKeyMap(ancestorStocks)

	for itemID, itemRecord := range stockMap[input.RepositoryID] {
		// Calculate stockMap for parents of the 'from' repository
		if err = s.simulateRepositoryStockMap(itemID, *input.FromID, input.ToID, -1*itemRecord.Quantity, stockMap, repositoryMap, false); err != nil {
			return nil, fmt.Errorf("failed calculating stock map: %w", err)
		}

		// Calculate stockMap for parents of the 'to' repository
		if err = s.simulateRepositoryStockMap(itemID, input.ToID, *input.FromID, itemRecord.Quantity, stockMap, repositoryMap, false); err != nil {
			return nil, fmt.Errorf("failed calculating stock map: %w", err)
		}
	}

	if dto.ValidateUniquenessHook != nil {
		if err = dto.ValidateUniquenessHook(); err != nil {
			return nil, err
		}
	}

	movement, err := tx.RepositoryMovement.
		Create().
		SetInput(input).
		SetExecuted(false).
		Save(ctx)
	if err != nil {
		return nil, err
	}

	versions := newStockVersionTracker(ancestorStocks)

	// ancestorStocks was captured by loadAncestorStocks (which filters
	// DeletedAtIsNil) before the movement INSERT above. The
	// stock_tenant_id_repository_id_item_id_version unique index is not
	// partial, so it also covers soft-deleted rows and any row a concurrent
	// transaction committed since that read. Re-seed the version floor from
	// the freshest max(version) per (repo, item) across that full universe so
	// nextFor cannot reuse a version another row already holds (which would
	// otherwise surface as an unrecoverable OCC duplicate-key conflict).
	maxVersions, err := s.loadMaxStockVersionsIncludingDeleted(ctx, tx, dto.TenantID, stockMap)
	if err != nil {
		return nil, err
	}
	versions.seedFromNested(maxVersions)

	// Only items physically present on the moving repository can have
	// their rolled-up stock change as a result of this movement; the
	// simulate walks above mutate IncomingStock / OutgoingStock /
	// OwnIncomingStock / OwnOutgoingStock only for items they iterate,
	// which is exactly stockMap[input.RepositoryID].
	//
	// Phase 4.7 narrows the closure stock READ to those items (see the
	// loadItemIDsAtRepo call above), so by construction stockMap only
	// contains entries for the moving repo's items. The in-Go filter
	// below is therefore redundant in the current control flow —
	// every (repoID, itemID) the outer loop visits is already in
	// movingItemIDs. We keep it as defense-in-depth: if a future
	// refactor drops the items filter passed to loadAncestorStocks
	// (Phase 4.7 backslide) or re-introduces ambient stockMap entries
	// from another source, this guard re-enforces the contract that
	// only-moving-repo items get fresh stock rows. Dropping the guard
	// would re-open the PostgreSQL 65,535-parameter wire-protocol
	// overflow that Phase 4.7 closed.
	movingItemIDs := make(map[uuid.UUID]struct{}, len(stockMap[input.RepositoryID]))
	for itemID := range stockMap[input.RepositoryID] {
		movingItemIDs[itemID] = struct{}{}
	}
	stocksToCreate := []*ent.StockCreate{}
	for repoID, stockRecord := range stockMap {
		for itemID := range stockRecord {
			if _, ok := movingItemIDs[itemID]; !ok {
				continue
			}
			newStock := tx.Stock.
				Create().
				SetItemID(itemID).
				SetTenantID(dto.TenantID).
				SetRepositoryID(repoID).
				SetQuantity(stockMap[repoID][itemID].Quantity).
				SetIncomingStock(stockMap[repoID][itemID].IncomingStock).
				SetOutgoingStock(stockMap[repoID][itemID].OutgoingStock).
				SetOwnQuantity(stockMap[repoID][itemID].OwnQuantity).
				SetOwnIncomingStock(stockMap[repoID][itemID].OwnIncomingStock).
				SetOwnOutgoingStock(stockMap[repoID][itemID].OwnOutgoingStock).
				SetMovementID(movement.ID).
				SetVersion(versions.nextFor(repoID, itemID))
			stocksToCreate = append(stocksToCreate, newStock)
		}
	}
	if _, err = tx.Stock.CreateBulk(stocksToCreate...).Save(ctx); err != nil {
		return nil, wrapOCCConflict(fmt.Errorf("failed inserting stock: %w", err))
	}

	return movement, nil
}

// ExecuteRepositoryMovement is the orchestration body that previously lived
// in the ExecuteInventoryRepositoryMovement resolver. The flow is verbatim:
// load the movement, reject already-executed movements, enforce
// position-ordered execution within the collection, flip the executed flag,
// load the moving repository, default the FromID to the moving repository
// when unset, fetch the per-item stock currently held by the moving
// repository, apply the FROM/TO parent-walk stock delta in a single pass
// per item (ownStock=false because items are not directly in the parents),
// fan out the resulting stock rows tagged with the movement ID, then
// re-parent the moved repository under the TO repository.
func (s *service) ExecuteRepositoryMovement(ctx context.Context, tx *ent.Tx, dto ExecuteRepositoryMovementInput) (*ent.RepositoryMovement, error) {
	id := dto.ID

	// Retrieve current values for repository movement
	currentMovementValues, err := tx.RepositoryMovement.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("repositoryMovement not found") //nolint:err113 // verbatim move from ExecuteInventoryRepositoryMovement resolver in Step 2.9.3e; tightening errors is out of scope.
	}

	// Executed mutation cannot be executed again
	if currentMovementValues.Executed {
		return nil, fmt.Errorf("RepositoryMovement is already executed") //nolint:err113 // verbatim move from ExecuteInventoryRepositoryMovement resolver in Step 2.9.3e; tightening errors is out of scope.
	}

	// Check position
	err = s.checkExecuteNextMovementByPosition(ctx, tx, currentMovementValues.CollectionID, currentMovementValues.Position)
	if err != nil {
		return nil, err
	}

	// Update 'executed' flag.
	//
	// The resolver did not check err from Save here; the next statement
	// reassigned err via repoToBeMoved, err := tx.Repository.Get and only the
	// Get error path was branched on. Preserving that verbatim is required by
	// the "no behavior change" guarantee for Step 2.9.3e; tightening it is
	// out of scope.
	//
	//nolint:ineffassign,wastedassign,staticcheck // verbatim shadow of err by the Get below.
	repositoryMovement, err := tx.RepositoryMovement.
		UpdateOneID(id).
		SetExecuted(true).
		SetExecutedAt(time.Now().UTC()).
		Save(ctx)

	repoToBeMoved, err := tx.Repository.Get(ctx, currentMovementValues.RepositoryID)
	if err != nil {
		return nil, fmt.Errorf("repository not found") //nolint:err113 // verbatim move from ExecuteInventoryRepositoryMovement resolver in Step 2.9.3e; tightening errors is out of scope.
	}

	if currentMovementValues.FromID == uuid.Nil {
		currentMovementValues.FromID = repoToBeMoved.ID
	}

	// Phase 4.6: collapse the legacy GetCurrentRepositoriesStock(moving
	// repo only) call and the targeted Phase 4.3 loadAncestorStocks call
	// into a single recursive-CTE load. Seeding with the moving
	// RepositoryID alongside [FromID, ToID] folds the per-item driver
	// read (the items currently held by the moving repository) into the
	// same query that hydrates the FROM/TO ancestor closure used by
	// applyItemMovementStockDelta. The ancestor-IDs filter keeps the read
	// bounded to just the closure (FINDINGS sections 3.2 and 3.5). The
	// flat map[stockKey]Stock is nested back into the legacy shape so we
	// can index by [RepositoryID] when iterating per-item below.
	//
	// Phase 4.7 (mirrored from CreateRepositoryMovement at impl.go:1016):
	// narrow the closure stock read to the items physically on the moving
	// repository. With items=nil the loader hydrated every (ancestor_repo,
	// item) pair in the closure; on tenants whose shared ancestors
	// (PACKING-ZONE, warehouse root) have accumulated rich unrelated stock
	// history this amplifies the read by orders of magnitude — the WF102
	// DeliverTrolleyToPackingZone hang walked ~12k latest rows across 61
	// LIMIT/OFFSET pages (~180–260s, past the ~117s gateway timeout) while
	// the per-item fan-out below discarded everything but the moving
	// repo's items anyway. The narrowing is behavior-preserving: the loop
	// only ever iterates nestStockKeyMap(priorStocks)[RepositoryID], and
	// loadItemIDsAtRepo uses the identical latest-row predicate, so the
	// hydrated set is exactly what items=nil produced for the moving repo.
	movingRepoItemIDs, err := s.loadItemIDsAtRepo(ctx, tx, dto.TenantID, currentMovementValues.RepositoryID)
	if err != nil {
		return nil, fmt.Errorf("load moving-repo item ids: %w", err)
	}
	repoMap, priorStocks, err := s.loadAncestorStocks(ctx, tx, dto.TenantID, []uuid.UUID{currentMovementValues.RepositoryID, currentMovementValues.FromID, currentMovementValues.ToID}, movingRepoItemIDs, false)
	if err != nil {
		return nil, fmt.Errorf("failed loading ancestor stocks: %w", err)
	}

	repositoryItemsStockMap := nestStockKeyMap(priorStocks)[currentMovementValues.RepositoryID]
	if repositoryItemsStockMap == nil {
		repositoryItemsStockMap = make(map[uuid.UUID]ent.Stock)
	}

	versions := newStockVersionTracker(priorStocks)
	stocksToCreate := []*ent.StockCreate{}
	for itemID, itemRecord := range repositoryItemsStockMap {
		// Create stock map. This map will contain all future Stock inserts
		stockMap := make(map[uuid.UUID]ent.Stock, 0)

		// Apply both FROM and TO walks in a single pass; validation is deferred
		// to avoid spurious underflow on common ancestors of FROM and TO (#1159).
		// Use ownStock=false because items are not directly in the parent, they're
		// in the moving repository.
		err = s.applyItemMovementStockDelta(itemID, currentMovementValues.FromID, currentMovementValues.ToID, itemRecord.Quantity, repoMap, priorStocks, stockMap, false)
		if err != nil {
			return nil, fmt.Errorf("failed calculating stock map: %w", err)
		}

		// Insert new stocks
		for repoID, repoRecord := range stockMap {
			newStock := tx.Stock.
				Create().
				SetItemID(itemID).
				SetTenantID(dto.TenantID).
				SetRepositoryID(repoID).
				SetQuantity(repoRecord.Quantity).
				SetCreatedBy(repositoryMovement.UpdatedBy).
				SetIncomingStock(repoRecord.IncomingStock).
				SetOutgoingStock(repoRecord.OutgoingStock).
				SetOwnQuantity(repoRecord.OwnQuantity).
				SetOwnIncomingStock(repoRecord.OwnIncomingStock).
				SetOwnOutgoingStock(repoRecord.OwnOutgoingStock).
				SetMovementID(id).
				SetVersion(versions.nextFor(repoID, itemID))
			stocksToCreate = append(stocksToCreate, newStock)
		}
	}
	_, err = tx.Stock.CreateBulk(stocksToCreate...).Save(ctx)
	if err != nil {
		return nil, wrapOCCConflict(fmt.Errorf("failed inserting stock: %w", err))
	}

	// Update parent of the moved repository
	_, err = tx.Repository.
		UpdateOneID(currentMovementValues.RepositoryID).
		SetParentID(currentMovementValues.ToID).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed updating repository parent: %w", err)
	}

	return repositoryMovement, nil
}

// DeleteRepositoryMovement is the orchestration body that previously lived
// in the DeleteInventoryRepositoryMovement resolver. The flow is verbatim:
// load the movement, reject already-executed movements with
// errDeleteExecutedMovement, walk parent maps for RepositoryID/ToID to
// scope the reservation reversal, query the original per-item stock
// snapshot tagged with this movement (the per-item quantities recorded at
// create time), simulate the FROM/TO stock-map deltas per item in
// subtract mode using those original quantities (ownStock=false because
// items are not directly in the parents), fan out the resulting stock
// rows tagged with the movement ID, then soft-delete the movement.
func (s *service) DeleteRepositoryMovement(ctx context.Context, tx *ent.Tx, dto DeleteRepositoryMovementInput) (*ent.RepositoryMovement, error) {
	id := dto.ID

	// Retrieve current values for repository movement
	movement, err := tx.RepositoryMovement.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("repositoryMovement not found") //nolint:err113 // verbatim move from DeleteInventoryRepositoryMovement resolver in Step 2.9.3f; tightening errors is out of scope.
	}

	// Cannot delete executed movements - they have already transferred actual stock
	if movement.Executed {
		return nil, errDeleteExecutedMovement
	}

	// Query original stock snapshot that was created with this movement
	// This contains the per-item quantities at the time the movement was created
	originalStockRecords, err := tx.Stock.Query().
		Where(entstock.MovementID(id)).
		Where(entstock.RepositoryID(movement.RepositoryID)).
		AllPages(ctx, mixin.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed reading original stock snapshot: %w", err)
	}

	// Build map of original quantities per item
	originalQuantities := make(map[uuid.UUID]int64)
	for _, record := range originalStockRecords {
		originalQuantities[record.ItemID] = record.Quantity
	}

	// Phase 4.6: replace the unbounded GetRepositoriesDetails +
	// GetCurrentRepositoriesStock pair with a single recursive-CTE
	// ancestor load seeded by [RepositoryID, FromID, ToID] and narrowed
	// to the items recorded in the movement's original snapshot. The
	// simulate walks only ever read parents of FROM and TO, so loading
	// the entire tenant's repository tree was wasted work once the LCA
	// cutoff landed in Phase 3 (FINDINGS sections 3.2 and 3.5). The
	// flat map[stockKey]Stock is nested back into the legacy shape via
	// nestStockKeyMap so simulateRepositoryStockMapWithMode and the
	// stock-fanout below stay untouched.
	itemIDs := make([]uuid.UUID, 0, len(originalQuantities))
	for itemID := range originalQuantities {
		itemIDs = append(itemIDs, itemID)
	}
	repositoryMap, ancestorStocks, err := s.loadAncestorStocks(ctx, tx, dto.TenantID, []uuid.UUID{movement.RepositoryID, movement.FromID, movement.ToID}, itemIDs, false)
	if err != nil {
		return nil, err
	}
	stockMap := nestStockKeyMap(ancestorStocks)

	// Reverse the stock reservations using subtract mode (same as item movement deletion)
	// Creation used: FromID=-qty (adds outgoing), ToID=+qty (adds incoming) with subtract=false
	// Deletion uses: FromID=-qty with subtract=true (removes outgoing), ToID=+qty with subtract=true (removes incoming)
	// CRITICAL: Use original quantities from snapshot, not current stock
	for itemID, originalQty := range originalQuantities {
		// FROM hierarchy: remove outgoing stock using same sign as create but with subtract=true
		if err = s.simulateRepositoryStockMapWithMode(itemID, movement.FromID, movement.ToID, -1*originalQty, stockMap, repositoryMap, false, true); err != nil {
			return nil, fmt.Errorf("failed calculating stock map: %w", err)
		}

		// TO hierarchy: remove incoming stock using same sign as create but with subtract=true
		if err = s.simulateRepositoryStockMapWithMode(itemID, movement.ToID, movement.FromID, originalQty, stockMap, repositoryMap, false, true); err != nil {
			return nil, fmt.Errorf("failed calculating stock map: %w", err)
		}
	}

	// Insert new stock records
	versions := newStockVersionTracker(ancestorStocks)

	// Same correction as CreateRepositoryMovement: ancestorStocks excludes
	// soft-deleted rows and predates the inserts, but the
	// stock_tenant_id_repository_id_item_id_version unique index covers the
	// full universe. Re-seed the version floor from the freshest max(version)
	// per (repo, item) so re-projecting after a delete cannot collide.
	maxVersions, err := s.loadMaxStockVersionsIncludingDeleted(ctx, tx, dto.TenantID, stockMap)
	if err != nil {
		return nil, err
	}
	versions.seedFromNested(maxVersions)

	stocksToCreate := []*ent.StockCreate{}
	for repo, stockRecord := range stockMap {
		for itemID := range stockRecord {
			newStock := tx.Stock.
				Create().
				SetItemID(itemID).
				SetTenantID(dto.TenantID).
				SetRepositoryID(repo).
				SetQuantity(stockMap[repo][itemID].Quantity).
				SetIncomingStock(stockMap[repo][itemID].IncomingStock).
				SetOutgoingStock(stockMap[repo][itemID].OutgoingStock).
				SetOwnQuantity(stockMap[repo][itemID].OwnQuantity).
				SetOwnIncomingStock(stockMap[repo][itemID].OwnIncomingStock).
				SetOwnOutgoingStock(stockMap[repo][itemID].OwnOutgoingStock).
				SetMovementID(id).
				SetVersion(versions.nextFor(repo, itemID))
			stocksToCreate = append(stocksToCreate, newStock)
		}
	}
	_, err = tx.Stock.CreateBulk(stocksToCreate...).Save(ctx)
	if err != nil {
		return nil, wrapOCCConflict(fmt.Errorf("failed inserting stock: %w", err))
	}

	deleted, err := tx.RepositoryMovement.
		UpdateOneID(id).
		SetDeletedAt(time.Now().UTC()).
		SetDeletedBy(dto.DeletedBy).
		Save(ctx)
	if err != nil {
		return nil, err
	}

	return deleted, nil
}

// CreateCollectionMovement orchestrates the create path for a multi-position
// collection movement. After Step 9.3 the body delegates to the direct
// CreateItemMovement / CreateRepositoryMovement service methods so the
// per-position bookkeeping (movement INSERT, ancestor stock fan-out,
// incoming/outgoing/quantity updates) lives in exactly one place. The
// remaining collection-only orchestration is:
//
//   - Per-position upfront validation: virtual-virtual rejection,
//     item-vs-repository guard, quantity guard. Running these before any
//     direct create keeps existing tests happy (they expect "no movement
//     persisted" semantics for these inputs).
//   - Persisting the collection_movement row + the optional pre-insert
//     uniqueness hook.
//   - Wrapping ctx with WithDeferredUnderflow for the per-position loop so
//     plan-ahead chains like A->B->C succeed even when B is initially
//     empty: each direct call records the movement plus
//     incoming/outgoing/quantity bookkeeping; only the per-call underflow
//     error is suppressed (FINDINGS §0.3 two-pass plan-ahead semantics).
//   - resolveCollectionRepositoryFromID for repository-movement positions
//     that omit FromID. The direct CreateRepositoryMovement only resolves
//     against the most recent non-executed movement; the collection path
//     also has to look back at earlier positions in the same collection
//     that target the same repository, which is collection-only glue.
//   - consistencyCheckSourceRows after the loop to validate the final
//     state of every touched source row in one shot. Returning a non-nil
//     error lets the surrounding gqltx middleware roll the tx back.
//
// The cross-collection reservation guard previously implemented by
// calculateReservedStockByCollectionID is gone: the direct path's
// outgoing-stock counter (incoming/outgoing on each ancestor stock row)
// is the same accounting collection-tier callers used to maintain
// separately.
func (s *service) CreateCollectionMovement(ctx context.Context, tx *ent.Tx, dto CreateCollectionMovementInput) (CreateCollectionMovementOutput, error) {
	result := CreateCollectionMovementOutput{
		ID:        dto.ID,
		Movements: make([]CreateCollectionMovementOutputItem, 0, len(dto.Collection)),
	}

	// Upfront per-position validation. Runs before any movement row is
	// persisted so a malformed collection (missing item/repo, invalid
	// quantity, virtual-to-virtual move) is rejected without leaving
	// orphan rows behind. The per-position direct-create path repeats
	// the virtual-repo guard, but the dual-virtual case is collection-
	// shaped and pinned by tests, so we keep it here.
	repositoryMap := make(map[uuid.UUID]ent.Repository, len(dto.Collection)*2)
	for i := range dto.Collection {
		collection := &dto.Collection[i]

		fromRepo, err := tx.Repository.Get(ctx, collection.FromID)
		if err != nil {
			return result, err
		}
		repositoryMap[fromRepo.ID] = *fromRepo

		toRepo, err := tx.Repository.Get(ctx, collection.ToID)
		if err != nil {
			return result, err
		}
		repositoryMap[toRepo.ID] = *toRepo

		if fromRepo.VirtualRepo && toRepo.VirtualRepo {
			return result, errors.New("movements between virtual repositories are not allowed") //nolint:err113 // verbatim move from CreateInventoryCollectionMovement resolver in Step 2.9.3g; tightening errors is out of scope.
		}

		if (collection.RepositoryID == nil && collection.ItemID == nil) ||
			(collection.RepositoryID != nil && collection.ItemID != nil) {
			return result, fmt.Errorf("repositoryID or itemID should be sent") //nolint:err113 // verbatim move from CreateInventoryCollectionMovement resolver in Step 2.9.3g.
		}

		if collection.ItemID != nil && (collection.Quantity == nil || *collection.Quantity <= 0) ||
			(collection.RepositoryID != nil && collection.Quantity != nil) {
			return result, fmt.Errorf("invalid quantity") //nolint:err113 // verbatim move from CreateInventoryCollectionMovement resolver in Step 2.9.3g.
		}

		if collection.RepositoryID != nil {
			repo, err := tx.Repository.Get(ctx, *collection.RepositoryID)
			if err != nil {
				return result, err
			}
			repositoryMap[repo.ID] = *repo
		}
	}

	// Persist the collection_movement row before any per-position movement
	// so the per-position rows can FK back to dto.ID.
	movement := tx.Collection_Movement.
		Create().
		SetID(dto.ID).
		SetNillableDataTypeID(dto.DataTypeID).
		SetData(dto.Data).
		SetTenantID(dto.TenantID).
		SetCreatedAt(time.Now().UTC())

	if dto.Handler != nil && *dto.Handler != "" {
		movement.SetHandler(*dto.Handler)
	}

	if _, err := movement.Save(ctx); err != nil {
		return result, err
	}

	if dto.PreInsertStockHook != nil {
		if err := dto.PreInsertStockHook(); err != nil {
			return result, err
		}
	}

	// Phase 9.3: route the per-position fan-out through the direct
	// service methods under WithDeferredUnderflow so each call writes
	// its own movement plus stock bookkeeping but suppresses the per-
	// call FROM-availability error. consistencyCheckSourceRows below
	// validates the final state of every touched source row in one
	// shot.
	deferredCtx := WithDeferredUnderflow(ctx)

	// Track the union of (sourceRepo, item) pairs the loop visits so
	// the post-loop consistency check can read back the final state of
	// every source row the chain depended on.
	sourceRepoSet := make(map[uuid.UUID]struct{}, len(dto.Collection))
	itemSet := make(map[uuid.UUID]struct{}, len(dto.Collection))

	for idx := range dto.Collection {
		collection := &dto.Collection[idx]
		switch {
		case collection.ItemID != nil:
			itemMov, err := s.CreateItemMovement(deferredCtx, tx, CreateItemMovementInput{
				Input: ent.CreateItemMovementInput{
					DataTypeID:   collection.DataTypeID,
					DataTypeSlug: collection.DataTypeSlug,
					Data:         collection.Data,
					Quantity:     int64(*collection.Quantity),
					Handler:      collection.Handler,
					FromID:       collection.FromID,
					ToID:         collection.ToID,
					ItemID:       *collection.ItemID,
					CollectionID: &dto.ID,
					Position:     &idx,
					OrderID:      collection.OrderID,
				},
				TenantID: dto.TenantID,
			})
			if err != nil {
				return result, fmt.Errorf("failed creating item movement: %w", err)
			}
			// consistencyCheckSourceRows is the post-loop counterpart
			// to WithDeferredUnderflow; virtual sources are excluded
			// because applyRepositoryStockDelta clamps them to zero
			// and they have no meaningful availability (the
			// CreateItemMovement direct path also short-circuits its
			// FROM gate on virtual sources). Without this filter, the
			// virtual-source-allowed scenario pinned by
			// TestCollectionMovement_CreateFromVirtualRepo trips a
			// false-positive underflow against the very source the
			// direct path intentionally never validated.
			if !repositoryMap[collection.FromID].VirtualRepo {
				sourceRepoSet[collection.FromID] = struct{}{}
				itemSet[*collection.ItemID] = struct{}{}
			}
			result.Movements = append(result.Movements, CreateCollectionMovementOutputItem{ID: itemMov.ID, MovementType: "itemMovement"})

		case collection.RepositoryID != nil:
			fromID := s.resolveCollectionRepositoryFromID(ctx, tx, dto, idx, collection, repositoryMap)
			if collection.FromID != uuid.Nil && collection.FromID != fromID {
				return result, fmt.Errorf("wrong FromID (%s) for repository (%s) movement. Expected FromID:%s", collection.FromID, collection.RepositoryID, fromID) //nolint:err113 // verbatim move from CreateInventoryCollectionMovement resolver in Step 2.9.3g.
			}
			collection.FromID = fromID
			repoMov, err := s.CreateRepositoryMovement(deferredCtx, tx, CreateRepositoryMovementInput{
				Input: ent.CreateRepositoryMovementInput{
					DataTypeID:   collection.DataTypeID,
					DataTypeSlug: collection.DataTypeSlug,
					Data:         collection.Data,
					Handler:      collection.Handler,
					ToID:         collection.ToID,
					FromID:       &collection.FromID,
					RepositoryID: *collection.RepositoryID,
					CollectionID: &dto.ID,
					Position:     &idx,
					OrderID:      collection.OrderID,
				},
				TenantID: dto.TenantID,
			})
			if err != nil {
				return result, err
			}
			// CreateRepositoryMovement has no FROM-availability gate
			// (a re-parent moves items as-is between subtrees rather
			// than transferring quantity), so there is no per-call
			// underflow error to defer for repo positions and nothing
			// for consistencyCheckSourceRows to validate. Item-movement
			// positions in the same collection do contribute to
			// sourceRepoSet above; the consistency pass still covers
			// every chain that actually moves quantity.
			result.Movements = append(result.Movements, CreateCollectionMovementOutputItem{ID: repoMov.ID, MovementType: "repositoryMovement"})
		}
	}

	if len(sourceRepoSet) > 0 {
		repoIDs := make([]uuid.UUID, 0, len(sourceRepoSet))
		for id := range sourceRepoSet {
			repoIDs = append(repoIDs, id)
		}
		itemIDs := make([]uuid.UUID, 0, len(itemSet))
		for id := range itemSet {
			itemIDs = append(itemIDs, id)
		}
		if err := s.consistencyCheckSourceRows(ctx, tx, dto.TenantID, repoIDs, itemIDs); err != nil {
			return result, err
		}
	}

	return result, nil
}

// resolveCollectionRepositoryFromID resolves the effective FromID for a
// repository-movement position: prefer the ToID of the most recent earlier
// position in the same collection that targets the same repository; fall
// back to the moving repository's parent, then to the most recent
// non-executed movement's ToID for the same repository. Verbatim from the
// previous resolver path.
func (s *service) resolveCollectionRepositoryFromID(
	ctx context.Context,
	tx *ent.Tx,
	dto CreateCollectionMovementInput,
	idx int,
	collection *CreateCollectionMovementCollectionInput,
	repositoryMap map[uuid.UUID]ent.Repository,
) uuid.UUID {
	var fromID uuid.UUID
	for i := range dto.Collection {
		c := &dto.Collection[i]
		if i < idx && c.RepositoryID != nil && *c.RepositoryID == *collection.RepositoryID {
			fromID = c.ToID
		}
	}

	if fromID != uuid.Nil {
		return fromID
	}

	fromID = repositoryMap[*collection.RepositoryID].ParentID

	lastRepoMovement, _ := tx.RepositoryMovement.Query().
		Where(
			entrepositorymovement.RepositoryID(*collection.RepositoryID),
			entrepositorymovement.Executed(false),
		).
		Order(ent.Desc(entrepositorymovement.FieldCreatedAt)).
		First(ctx)

	if lastRepoMovement != nil {
		fromID = lastRepoMovement.ToID
	}
	return fromID
}

// DeleteCollection is the orchestration body that previously lived in the
// DeleteInventoryCollection resolver. The flow is verbatim: verify the
// collection exists, count non-deleted item movements and repository
// movements still attached to the collection (return
// errCollectionHasMovements if any remain), and soft-delete the
// collection_movement row.
func (s *service) DeleteCollection(ctx context.Context, tx *ent.Tx, dto DeleteCollectionInput) (*ent.Collection_Movement, error) {
	id := dto.ID

	// Verify the collection exists
	if _, err := tx.Collection_Movement.Get(ctx, id); err != nil {
		return nil, fmt.Errorf("inventoryCollection not found") //nolint:err113 // verbatim move from DeleteInventoryCollection resolver in Step 2.9.3g; tightening errors is out of scope.
	}

	// Cannot delete a collection that still has item movements
	itemMovementCount, err := tx.ItemMovement.Query().
		Where(entitemmovement.CollectionID(id), entitemmovement.DeletedAtIsNil()).
		Count(ctx)
	if err != nil {
		return nil, err
	}
	if itemMovementCount > 0 {
		return nil, errCollectionHasMovements
	}

	// Cannot delete a collection that still has repository movements
	repoMovementCount, err := tx.RepositoryMovement.Query().
		Where(entrepositorymovement.CollectionID(id), entrepositorymovement.DeletedAtIsNil()).
		Count(ctx)
	if err != nil {
		return nil, err
	}
	if repoMovementCount > 0 {
		return nil, errCollectionHasMovements
	}

	deleted, err := tx.Collection_Movement.UpdateOneID(id).
		SetDeletedAt(time.Now().UTC()).
		SetDeletedBy(dto.DeletedBy).
		Save(ctx)
	if err != nil {
		return nil, err
	}

	return deleted, nil
}

// insertStockMap fans out the simulate result into stock rows tagged with
// movementID. Entries whose six quantity fields exactly match the most
// recent existing stock row for the same (repository, item) pair are
// skipped: re-inserting an identical snapshot is a no-op write that only
// inflates the append-only ledger (FINDINGS section 3.4). Phase 3.2/3.3's
// LCA cutoff already trims most no-ops, but a few can still slip through
// (e.g., DELETE-mode reverts on rows that were already clamped to zero,
// or sibling cancellations at intermediate ancestors).
//
// The baseline is loaded once via a single batched query keyed by
// (repository_id IN ..., item_id = itemID) using a NOT EXISTS subquery to
// pick the latest row per repository. No per-pair queries fire inside the
// loop. DistinctOnExists is intentionally not used here — that helper is
// removed in a later step.
func (s *service) insertStockMap(ctx context.Context, tx *ent.Tx, itemID, tenantID, movementID uuid.UUID, stockMap map[uuid.UUID]map[uuid.UUID]ent.Stock) error {
	return s.insertStockMapWithVersions(ctx, tx, itemID, tenantID, movementID, stockMap, nil)
}

// insertStockMapWithVersions is the version-aware extension of
// insertStockMap. When versions is non-nil, new rows draw their versions
// from the supplied tracker (which the caller maintains across multiple
// inserts within the same transaction — e.g. a CreateCollectionMovement
// loop). When versions is nil, the helper loads the latest version per
// (repo, item) freshly from the DB and seeds an internal tracker — that
// covers the standalone callers that only insert one batch per
// transaction.
func (s *service) insertStockMapWithVersions(ctx context.Context, tx *ent.Tx, itemID, tenantID, movementID uuid.UUID, stockMap map[uuid.UUID]map[uuid.UUID]ent.Stock, versions *stockVersionTracker) error {
	if len(stockMap) == 0 {
		return nil
	}

	repoIDs := make([]uuid.UUID, 0, len(stockMap))
	for repo := range stockMap {
		repoIDs = append(repoIDs, repo)
	}

	latest, err := s.loadLatestStockPerRepo(ctx, tx, tenantID, itemID, repoIDs)
	if err != nil {
		return err
	}

	if versions == nil {
		// Seed the per-tx version tracker from the latest row per repo.
		// The helper keys on (repo, item) and itemID is fixed for this
		// call, so re-pack the per-repo map into the flat shape the
		// tracker expects.
		flatLatest := make(map[stockKey]ent.Stock, len(latest))
		for repoID, rec := range latest {
			flatLatest[stockKey{RepositoryID: repoID, ItemID: itemID}] = rec
		}
		versions = newStockVersionTracker(flatLatest)
	} else {
		// Caller supplied a shared tracker; merge in whatever DB values
		// we just observed so any rows committed by an earlier helper
		// invocation are reflected. seedFromNested takes max of current
		// and observed+1, so this is monotonic.
		nested := make(map[uuid.UUID]map[uuid.UUID]ent.Stock, len(latest))
		for repoID, rec := range latest {
			nested[repoID] = map[uuid.UUID]ent.Stock{itemID: rec}
		}
		versions.seedFromNested(nested)
	}

	stocksToCreate := []*ent.StockCreate{}
	for repo := range stockMap {
		// Source the In/Out counters from stockMap (which carries the
		// simulate walk's deltas) but source Quantity / OwnQuantity from
		// the freshly-loaded `latest` map (the loadLatestStockPerRepo
		// call above) rather than from stockMap.
		// simulateRepositoryStockMapWalk only mutates the four In/Out
		// fields; Quantity and OwnQuantity on stockMap are pure
		// passthrough from the much-earlier loadAncestorStocks read
		// (e.g. in createItemMovementViaGo / DeleteItemMovement). Under
		// READ COMMITTED that earlier read can be stale relative to
		// `latest` if a concurrent tx committed a new stocks row for the
		// same (repo, item) in between, so taking Q / OwnQ from `latest`
		// is what makes the INSERT consistent with the version we are
		// about to assign. In the no-race case the two reads agree and
		// behavior is unchanged. See
		// issue-stock-map-resets-own-quantity-to-zero-on-pending-pick-creation.md
		// and stocks_race_test.go for the regression that pins this.
		quantity := int64(0)
		incomingQuantity := int64(0)
		outgoingQuantity := int64(0)
		ownQuantity := int64(0)
		ownIncomingQuantity := int64(0)
		ownOutgoingQuantity := int64(0)
		if stockMap[repo] != nil {
			incomingQuantity = stockMap[repo][itemID].IncomingStock
			outgoingQuantity = stockMap[repo][itemID].OutgoingStock
			ownIncomingQuantity = stockMap[repo][itemID].OwnIncomingStock
			ownOutgoingQuantity = stockMap[repo][itemID].OwnOutgoingStock
		}
		if l, ok := latest[repo]; ok {
			quantity = l.Quantity
			ownQuantity = l.OwnQuantity
		}

		// Skip when every quantity field matches the most recent existing
		// row for this (repo, item). When no prior row exists, the
		// implicit baseline is a zero-valued ent.Stock — that matches how
		// applyRepositoryStockDelta and simulateRepositoryStockMap treat
		// unseen baselines, so an entry of all zeros against a missing
		// row is also a genuine no-op (writing "this is still 0" carries
		// no information).
		prev := latest[repo]
		if prev.Quantity == quantity &&
			prev.IncomingStock == incomingQuantity &&
			prev.OutgoingStock == outgoingQuantity &&
			prev.OwnQuantity == ownQuantity &&
			prev.OwnIncomingStock == ownIncomingQuantity &&
			prev.OwnOutgoingStock == ownOutgoingQuantity {
			continue
		}

		newStock := tx.Stock.
			Create().
			SetItemID(itemID).
			SetTenantID(tenantID).
			SetRepositoryID(repo).
			SetQuantity(quantity).
			SetIncomingStock(incomingQuantity).
			SetOutgoingStock(outgoingQuantity).
			SetOwnQuantity(ownQuantity).
			SetOwnIncomingStock(ownIncomingQuantity).
			SetOwnOutgoingStock(ownOutgoingQuantity).
			SetMovementID(movementID).
			SetVersion(versions.nextFor(repo, itemID))
		stocksToCreate = append(stocksToCreate, newStock)
	}
	if len(stocksToCreate) == 0 {
		return nil
	}
	if _, err := tx.Stock.CreateBulk(stocksToCreate...).Save(ctx); err != nil {
		return wrapOCCConflict(fmt.Errorf("failed inserting stocks: %w", err))
	}

	return nil
}

// stockVersionTracker assigns monotonically increasing per-(repo, item)
// version numbers to new stock rows within a single transaction. Phase 6.1
// added a unique index on (tenant_id, repository_id, item_id, version) to
// the stocks ledger, so every INSERT must pick a fresh version for its
// group; the simplest correct shape is a per-tx counter seeded from the
// most recently committed version per group (which the caller has already
// loaded via loadAncestorStocks or loadLatestStockPerRepo).
//
// Phase 6.2 will extend this to detect a 23505 unique-violation against the
// new index name (stock_tenant_id_repository_id_item_id_version) on the
// source-row write and surface it as errOCCConflict; the bookkeeping here
// is just the version-assignment half of that protocol.
type stockVersionTracker struct {
	next map[stockKey]int64
}

// newStockVersionTracker seeds a tracker from a flat latest-version map
// (the shape returned by loadAncestorStocks). For groups with no
// pre-existing row, the tracker reports version 0 on first call.
func newStockVersionTracker(latest map[stockKey]ent.Stock) *stockVersionTracker {
	next := make(map[stockKey]int64, len(latest))
	for k, v := range latest {
		next[k] = v.Version + 1
	}
	return &stockVersionTracker{next: next}
}

// nextFor returns the version to assign to a new stock row for the given
// (repository, item) pair and increments the internal counter so a
// subsequent insert for the same group within the same transaction
// receives a strictly higher value. The very first call for an unknown
// group returns 0 (matching the schema default).
func (t *stockVersionTracker) nextFor(repoID, itemID uuid.UUID) int64 {
	k := stockKey{RepositoryID: repoID, ItemID: itemID}
	v := t.next[k]
	t.next[k] = v + 1
	return v
}

// seedFromNested merges a nested stockMap (repoID → itemID → ent.Stock)
// into the tracker, using each entry's Version as the floor. Used by
// callers that loaded their baseline through GetCurrentRepositoriesStock
// (which returns the nested shape) instead of loadAncestorStocks.
func (t *stockVersionTracker) seedFromNested(stockMap map[uuid.UUID]map[uuid.UUID]ent.Stock) {
	for repoID, perItem := range stockMap {
		for itemID, rec := range perItem {
			k := stockKey{RepositoryID: repoID, ItemID: itemID}
			candidate := rec.Version + 1
			if cur, ok := t.next[k]; !ok || candidate > cur {
				t.next[k] = candidate
			}
		}
	}
}

// maxStockRowsPerInsert caps stocks-table CreateBulk batches at a row count
// safely below PostgreSQL's 65,535-parameter wire-protocol limit (the hard
// int16 ceiling in the extended-query Bind message).
//
// The rebuild path's StockCreate sets 14 columns per row (id, tenant_id,
// created_at, created_by, item_id, repository_id, quantity, movement_id,
// incoming_stock, outgoing_stock, own_quantity, own_incoming_stock,
// own_outgoing_stock, version), so floor(65535/14) = 4,681. 4,500 leaves
// headroom for future column additions without having to re-derive the
// constant when a new field gets bolted on to the stocks schema.
//
// Regression for pyck-ai/pyck#1227; the live-mutation paths
// (CreateRepositoryMovement / DeleteRepositoryMovement) were already
// addressed by item-narrowing in commits 60ee4a34, fb5e65fb, 2115e547,
// and 131b6fe8.
const maxStockRowsPerInsert = 4500

// stockBulkChunkBounds returns the contiguous [start, end) ranges that
// partition a slice of length n into batches of at most maxBatch elements.
// The function is pure (no I/O, no side effects) so its slicing math can
// be exhaustively unit-tested without spinning up a database.
//
// Empty input yields a nil result; the caller's `for _, b := range ...`
// loop becomes a zero-iteration no-op.
func stockBulkChunkBounds(n, maxBatch int) [][2]int {
	if n <= 0 || maxBatch <= 0 {
		return nil
	}
	out := make([][2]int, 0, (n+maxBatch-1)/maxBatch)
	for start := 0; start < n; start += maxBatch {
		end := start + maxBatch
		if end > n {
			end = n
		}
		out = append(out, [2]int{start, end})
	}
	return out
}

// stockCreateBulkChunked saves stocksToCreate in successive CreateBulk
// batches small enough to stay under PostgreSQL's 65,535-parameter wire
// limit. All batches share the caller's transaction, so a failure mid-loop
// rolls back the whole rebuild atomically via the surrounding gqltx
// middleware — there is no partial-write window.
//
// Empty input is a no-op (returns nil without touching the database).
func stockCreateBulkChunked(ctx context.Context, tx *ent.Tx, stocksToCreate []*ent.StockCreate) error {
	for _, b := range stockBulkChunkBounds(len(stocksToCreate), maxStockRowsPerInsert) {
		if _, err := tx.Stock.CreateBulk(stocksToCreate[b[0]:b[1]]...).Save(ctx); err != nil {
			return fmt.Errorf("CreateBulk batch %d-%d of %d: %w", b[0], b[1], len(stocksToCreate), err)
		}
	}
	return nil
}

// loadLatestStockPerRepo returns, for a fixed itemID and a slice of
// repository IDs, the most recent stock row per repository within the
// tenant. Issues exactly one batched query (a single round trip) that
// uses a NOT EXISTS subquery to pick the latest row per (repository_id,
// item_id). Pairs without any prior row are simply absent from the
// returned map — callers must treat the missing case as "all fields are
// zero" (which is consistent with how applyRepositoryStockDelta /
// simulateRepositoryStockMap treat unseen baselines).
func (s *service) loadLatestStockPerRepo(ctx context.Context, tx *ent.Tx, tenantID, itemID uuid.UUID, repoIDs []uuid.UUID) (map[uuid.UUID]ent.Stock, error) {
	if len(repoIDs) == 0 {
		return map[uuid.UUID]ent.Stock{}, nil
	}

	// Inline a NOT EXISTS subquery (same shape DistinctOnExists generates)
	// rather than calling that helper: DistinctOnExists is removed in a
	// later step, so we keep the shape decoupled from it.
	latestPredicate := entpredicate.Stock(func(sel *sql.Selector) {
		t := sql.Table(entstock.Table).As("s2")
		sub := sql.SelectExpr(sql.Expr("1")).From(t).Where(sql.And(
			sql.ColumnsEQ(t.C(entstock.RepositoryColumn), sel.C(entstock.RepositoryColumn)),
			sql.ColumnsEQ(t.C(entstock.ItemColumn), sel.C(entstock.ItemColumn)),
			sql.ColumnsGT(t.C(entstock.FieldCreatedAt), sel.C(entstock.FieldCreatedAt)),
		))
		sel.Where(sql.Not(sql.Exists(sub)))
	})

	records, err := tx.Stock.Query().
		Where(
			entstock.TenantID(tenantID),
			entstock.ItemID(itemID),
			entstock.RepositoryIDIn(repoIDs...),
			latestPredicate,
		).
		AllPages(ctx, mixin.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed reading latest stock baseline: %w", err)
	}

	out := make(map[uuid.UUID]ent.Stock, len(records))
	for _, r := range records {
		out[r.RepositoryID] = *r
	}
	return out, nil
}

// loadMaxStockVersionsIncludingDeleted returns, per (repo, item) appearing in
// stockMap, a pseudo ent.Stock carrying the highest version currently on the
// stocks ledger INCLUDING soft-deleted rows. seedFromNested consumes these as
// a per-group version floor. Unlike loadAncestorStocks / loadLatestStockPerRepo
// it deliberately omits the DeletedAtIsNil filter: the
// stock_tenant_id_repository_id_item_id_version unique index is not partial, so
// the next version must clear soft-deleted rows too. Reading it here — right
// before the fan-out INSERT — also narrows the window in which a concurrently
// committed row could be missed.
func (s *service) loadMaxStockVersionsIncludingDeleted(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, stockMap map[uuid.UUID]map[uuid.UUID]ent.Stock) (map[uuid.UUID]map[uuid.UUID]ent.Stock, error) {
	repoSet := make(map[uuid.UUID]struct{}, len(stockMap))
	itemSet := make(map[uuid.UUID]struct{})
	for repoID, perItem := range stockMap {
		repoSet[repoID] = struct{}{}
		for itemID := range perItem {
			itemSet[itemID] = struct{}{}
		}
	}
	if len(repoSet) == 0 || len(itemSet) == 0 {
		return make(map[uuid.UUID]map[uuid.UUID]ent.Stock), nil
	}

	repoIDs := make([]uuid.UUID, 0, len(repoSet))
	for r := range repoSet {
		repoIDs = append(repoIDs, r)
	}
	itemIDs := make([]uuid.UUID, 0, len(itemSet))
	for i := range itemSet {
		itemIDs = append(itemIDs, i)
	}

	rows, err := tx.Stock.Query().
		Where(
			entstock.TenantID(tenantID),
			entstock.RepositoryIDIn(repoIDs...),
			entstock.ItemIDIn(itemIDs...),
		).
		AllPages(ctx, mixin.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed reading max stock version baseline: %w", err)
	}

	out := make(map[uuid.UUID]map[uuid.UUID]ent.Stock)
	for _, r := range rows {
		perItem := out[r.RepositoryID]
		if perItem == nil {
			perItem = make(map[uuid.UUID]ent.Stock)
			out[r.RepositoryID] = perItem
		}
		if cur, ok := perItem[r.ItemID]; !ok || r.Version > cur.Version {
			perItem[r.ItemID] = *r
		}
	}
	return out, nil
}

// ── Rebuild stock table ───────────────────────────────────────────────────────

// rebuildEventKind distinguishes the six event types produced during a stock rebuild.
// The iota ordering is significant: pending (0) < execute/delete (>0) so that
// the sort tiebreaker places creation events before follow-up events at the same
// timestamp.
type rebuildEventKind int

const (
	rebuildPendingItem rebuildEventKind = iota // item movement created, not executed or deleted
	rebuildExecuteItem                         // item movement executed
	rebuildDeleteItem                          // item movement deleted (reservation released)
	rebuildPendingRepo                         // repo movement created, not executed or deleted
	rebuildExecuteRepo                         // repo movement executed
	rebuildDeleteRepo                          // repo movement deleted (reservation released)
)

// rebuildEvent is a single stock-table write event to replay.
// A movement produces 1 event (pending only) or 2 events (pending + execute/delete).
type rebuildEvent struct {
	timestamp time.Time
	kind      rebuildEventKind
	itemMov   *ent.ItemMovement
	repoMov   *ent.RepositoryMovement
}

// RebuildStockTable clears all stock rows for the tenant and reconstructs them
// by replaying every movement event in chronological order, calling the same
// stock calculation functions used by the original create/execute/delete mutations.
//
// Three-pointer approach:
//
//   - A = created_at  where executed_at IS NULL AND deleted_at IS NULL  → PENDING reservation
//   - B = executed_at where deleted_at IS NULL                          → EXECUTE
//   - C = deleted_at                                                    → DELETE / release
//
// deleted_at is checked before executed_at because Ent's UpdateDefault(time.Now)
// on executed_at fires spuriously during soft-delete mutations, leaving deleted
// movements with both fields set. deleted_at is the definitive lifecycle indicator.
//
// Must be called within a transaction.
func (s *service) RebuildStockTable(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID) error {
	// showDeletedCtx bypasses the HistoryMixin soft-delete filter.
	showDeletedCtx := feature.Context(ctx, feature.FEATURE_SHOW_DELETED)

	// Step 1 – delete all existing stock rows for this tenant.
	if _, err := tx.Stock.Delete().Where(entstock.TenantID(tenantID)).Exec(ctx); err != nil {
		return fmt.Errorf("failed to delete stock records: %w", err)
	}

	// Step 2 – load ALL repository movements (including soft-deleted).
	// Paginate because LimitMixin enforces a hard cap of 200 rows per query.
	const rebuildPageSize = 200

	var allRepoMovs []*ent.RepositoryMovement
	for offset := 0; ; offset += rebuildPageSize {
		page, pageErr := tx.RepositoryMovement.Query().
			Where(entrepositorymovement.TenantID(tenantID)).
			Order(entrepositorymovement.ByCreatedAt(), entrepositorymovement.ByID()).
			Limit(rebuildPageSize).
			Offset(offset).
			All(showDeletedCtx)
		if pageErr != nil {
			return fmt.Errorf("failed to query repository movements (offset=%d): %w", offset, pageErr)
		}

		allRepoMovs = append(allRepoMovs, page...)

		if len(page) < rebuildPageSize {
			break
		}
	}
	var err error

	// Step 3 – pre-validate that every referenced repository exists.
	repoMap, err := s.GetRepositoriesDetails(showDeletedCtx, tx)
	if err != nil {
		return fmt.Errorf("failed to load repository map: %w", err)
	}
	for _, mov := range allRepoMovs {
		if _, ok := repoMap[mov.RepositoryID]; !ok {
			return fmt.Errorf("movement %s: %w: %s", mov.ID, errRebuildRepoNotFoundInRepoMap, mov.RepositoryID)
		}
	}

	// Step 4 – rewind executed (non-deleted) repository movements in reverse
	// executed_at order, restoring each repository's parent_id to its
	// pre-execution value (FromID).
	var executedRepoMovs []*ent.RepositoryMovement
	for _, mov := range allRepoMovs {
		if mov.ExecutedAt != nil && mov.DeletedAt.IsZero() {
			executedRepoMovs = append(executedRepoMovs, mov)
		}
	}
	sort.Slice(executedRepoMovs, func(i, j int) bool {
		ti, tj := *executedRepoMovs[i].ExecutedAt, *executedRepoMovs[j].ExecutedAt
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return rebuildCmpUUID(executedRepoMovs[i].ID, executedRepoMovs[j].ID) < 0
	})
	for i := len(executedRepoMovs) - 1; i >= 0; i-- {
		mov := executedRepoMovs[i]
		var fromPtr *uuid.UUID
		if mov.FromID != uuid.Nil {
			id := mov.FromID
			fromPtr = &id
		}
		if _, err = tx.Repository.UpdateOneID(mov.RepositoryID).SetNillableParentID(fromPtr).Save(showDeletedCtx); err != nil {
			return fmt.Errorf("failed rewinding parent of repository %s (movement %s): %w", mov.RepositoryID, mov.ID, err)
		}
	}

	// Step 5 – load ALL item movements (including soft-deleted).
	// Paginate because LimitMixin enforces a hard cap of 200 rows per query.
	var allItemMovs []*ent.ItemMovement
	for offset := 0; ; offset += rebuildPageSize {
		page, pageErr := tx.ItemMovement.Query().
			Where(entitemmovement.TenantID(tenantID)).
			Order(entitemmovement.ByCreatedAt(), entitemmovement.ByID()).
			Limit(rebuildPageSize).
			Offset(offset).
			All(showDeletedCtx)
		if pageErr != nil {
			return fmt.Errorf("failed to query item movements (offset=%d): %w", offset, pageErr)
		}

		allItemMovs = append(allItemMovs, page...)

		if len(page) < rebuildPageSize {
			break
		}
	}

	// Step 6 – build the sorted event list.
	events := make([]rebuildEvent, 0, (len(allItemMovs)+len(allRepoMovs))*2)

	for _, mov := range allItemMovs {
		switch {
		case !mov.DeletedAt.IsZero():
			// DELETED: deleted_at checked first — executed_at may be spuriously set
			// by Ent's UpdateDefault when the soft-delete mutation fires.
			events = append(events,
				rebuildEvent{timestamp: mov.CreatedAt, kind: rebuildPendingItem, itemMov: mov},
				rebuildEvent{timestamp: mov.DeletedAt, kind: rebuildDeleteItem, itemMov: mov},
			)
		case mov.ExecutedAt != nil:
			// EXECUTED
			events = append(events,
				rebuildEvent{timestamp: mov.CreatedAt, kind: rebuildPendingItem, itemMov: mov},
				rebuildEvent{timestamp: *mov.ExecutedAt, kind: rebuildExecuteItem, itemMov: mov},
			)
		default:
			// PENDING
			events = append(events,
				rebuildEvent{timestamp: mov.CreatedAt, kind: rebuildPendingItem, itemMov: mov},
			)
		}
	}

	for _, mov := range allRepoMovs {
		switch {
		case !mov.DeletedAt.IsZero():
			events = append(events,
				rebuildEvent{timestamp: mov.CreatedAt, kind: rebuildPendingRepo, repoMov: mov},
				rebuildEvent{timestamp: mov.DeletedAt, kind: rebuildDeleteRepo, repoMov: mov},
			)
		case mov.ExecutedAt != nil:
			events = append(events,
				rebuildEvent{timestamp: mov.CreatedAt, kind: rebuildPendingRepo, repoMov: mov},
				rebuildEvent{timestamp: *mov.ExecutedAt, kind: rebuildExecuteRepo, repoMov: mov},
			)
		default:
			events = append(events,
				rebuildEvent{timestamp: mov.CreatedAt, kind: rebuildPendingRepo, repoMov: mov},
			)
		}
	}

	// Sort by timestamp asc, then kind asc (pending=0 < execute/delete), then UUID.
	sort.Slice(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if !a.timestamp.Equal(b.timestamp) {
			return a.timestamp.Before(b.timestamp)
		}
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		return rebuildCmpUUID(rebuildEventID(a), rebuildEventID(b)) < 0
	})

	// Step 7 – replay each event, writing stock rows to the DB after each.
	for i, evt := range events {
		if err = s.replayRebuildEvent(ctx, showDeletedCtx, tx, tenantID, i, evt); err != nil {
			return err
		}
	}

	return nil
}

// replayRebuildEvent replays a single rebuild event by calling the same stock
// calculation functions used by the original mutations.
func (s *service) replayRebuildEvent(
	ctx, showDeletedCtx context.Context,
	tx *ent.Tx,
	tenantID uuid.UUID,
	evtIdx int,
	evt rebuildEvent,
) error {
	switch evt.kind {
	// ── PENDING ITEM: mirrors CreateItemMovement stock logic ──────────────────
	case rebuildPendingItem:
		mov := evt.itemMov
		s.debugf("EVENT[%d] PENDING_ITEM mov=%s item=%s from=%s to=%s qty=%d ts=%s",
			evtIdx, mov.ID, mov.ItemID, mov.FromID, mov.ToID, mov.Quantity, evt.timestamp)

		repositoryMap, err := s.GetRepositoriesDetails(showDeletedCtx, tx)
		if err != nil {
			return fmt.Errorf("movement %s: failed loading repository map: %w", mov.ID, err)
		}
		parentsMap := make(map[uuid.UUID]ent.Repository)
		if err = s.getRepositoriesParentsDetails(mov.FromID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (from): %w", mov.ID, err)
		}
		if err = s.getRepositoriesParentsDetails(mov.ToID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (to): %w", mov.ID, err)
		}
		stockMap := map[uuid.UUID]map[uuid.UUID]ent.Stock{}
		if repoIDs := rebuildMapKeys(parentsMap); len(repoIDs) > 0 {
			if stockMap, err = s.GetCurrentRepositoriesStock(ctx, tx, repoIDs); err != nil {
				return fmt.Errorf("movement %s: GetCurrentRepositoriesStock: %w", mov.ID, err)
			}
		}
		if err = s.simulateRepositoryStockMap(mov.ItemID, mov.FromID, mov.ToID, -mov.Quantity, stockMap, repositoryMap, true); err != nil {
			return fmt.Errorf("movement %s: SimulateRepositoryStockMap (from): %w", mov.ID, err)
		}
		if err = s.simulateRepositoryStockMap(mov.ItemID, mov.ToID, mov.FromID, mov.Quantity, stockMap, repositoryMap, true); err != nil {
			return fmt.Errorf("movement %s: SimulateRepositoryStockMap (to): %w", mov.ID, err)
		}
		if err = s.insertStockMap(ctx, tx, mov.ItemID, tenantID, mov.ID, stockMap); err != nil {
			return fmt.Errorf("movement %s: InsertStockMap: %w", mov.ID, err)
		}

	// ── EXECUTE ITEM: mirrors ExecuteItemMovement stock logic ────────────────
	case rebuildExecuteItem:
		mov := evt.itemMov
		s.debugf("EVENT[%d] EXECUTE_ITEM mov=%s item=%s from=%s to=%s qty=%d ts=%s",
			evtIdx, mov.ID, mov.ItemID, mov.FromID, mov.ToID, mov.Quantity, evt.timestamp)

		// Phase 4.3: load the FROM/TO ancestor closure once, narrowed to
		// the moving item, then drive the executor walk against the
		// pre-loaded maps. Mirrors the ExecuteItemMovement caller; the
		// rebuild path uses showDeletedCtx so soft-deleted ancestor repos
		// are still visible during replay.
		repoMap, priorStocks, err := s.loadAncestorStocks(showDeletedCtx, tx, tenantID, []uuid.UUID{mov.FromID, mov.ToID}, []uuid.UUID{mov.ItemID}, true)
		if err != nil {
			return fmt.Errorf("movement %s: loadAncestorStocks: %w", mov.ID, err)
		}
		stockMap := make(map[uuid.UUID]ent.Stock)
		if err := s.applyItemMovementStockDelta(mov.ItemID, mov.FromID, mov.ToID, mov.Quantity, repoMap, priorStocks, stockMap, true); err != nil {
			return fmt.Errorf("movement %s: ApplyItemMovementStockDelta: %w", mov.ID, err)
		}
		versions := newStockVersionTracker(priorStocks)
		creates := make([]*ent.StockCreate, 0, len(stockMap))
		for repoID, rec := range stockMap {
			creates = append(creates, tx.Stock.Create().
				SetItemID(mov.ItemID).SetTenantID(tenantID).SetRepositoryID(repoID).
				SetQuantity(rec.Quantity).SetIncomingStock(rec.IncomingStock).SetOutgoingStock(rec.OutgoingStock).
				SetOwnQuantity(rec.OwnQuantity).SetOwnIncomingStock(rec.OwnIncomingStock).SetOwnOutgoingStock(rec.OwnOutgoingStock).
				SetMovementID(mov.ID).
				SetVersion(versions.nextFor(repoID, mov.ItemID)))
		}
		if len(creates) > 0 {
			if err := stockCreateBulkChunked(ctx, tx, creates); err != nil {
				return fmt.Errorf("movement %s: CreateBulk execute-item: %w", mov.ID, err)
			}
		}

	// ── DELETE ITEM: mirrors DeleteItemMovement stock logic ──────────────────
	case rebuildDeleteItem:
		mov := evt.itemMov
		s.debugf("EVENT[%d] DELETE_ITEM mov=%s item=%s from=%s to=%s qty=%d ts=%s",
			evtIdx, mov.ID, mov.ItemID, mov.FromID, mov.ToID, mov.Quantity, evt.timestamp)

		repositoryMap, err := s.GetRepositoriesDetails(showDeletedCtx, tx)
		if err != nil {
			return fmt.Errorf("movement %s: failed loading repository map: %w", mov.ID, err)
		}
		parentsMap := make(map[uuid.UUID]ent.Repository)
		if err = s.getRepositoriesParentsDetails(mov.FromID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (from): %w", mov.ID, err)
		}
		if err = s.getRepositoriesParentsDetails(mov.ToID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (to): %w", mov.ID, err)
		}
		stockMap := map[uuid.UUID]map[uuid.UUID]ent.Stock{}
		if repoIDs := rebuildMapKeys(parentsMap); len(repoIDs) > 0 {
			if stockMap, err = s.GetCurrentRepositoriesStock(ctx, tx, repoIDs); err != nil {
				return fmt.Errorf("movement %s: GetCurrentRepositoriesStock: %w", mov.ID, err)
			}
		}
		if err = s.simulateRepositoryStockMapWithMode(mov.ItemID, mov.FromID, mov.ToID, -mov.Quantity, stockMap, repositoryMap, true, true); err != nil {
			return fmt.Errorf("movement %s: SimulateRepositoryStockMapWithMode (from): %w", mov.ID, err)
		}
		if err = s.simulateRepositoryStockMapWithMode(mov.ItemID, mov.ToID, mov.FromID, mov.Quantity, stockMap, repositoryMap, true, true); err != nil {
			return fmt.Errorf("movement %s: SimulateRepositoryStockMapWithMode (to): %w", mov.ID, err)
		}
		if err = s.insertStockMap(ctx, tx, mov.ItemID, tenantID, mov.ID, stockMap); err != nil {
			return fmt.Errorf("movement %s: InsertStockMap: %w", mov.ID, err)
		}

	// ── PENDING REPO: mirrors CreateRepositoryMovement stock logic ────────────
	case rebuildPendingRepo:
		mov := evt.repoMov
		s.debugf("EVENT[%d] PENDING_REPO mov=%s repo=%s from=%s to=%s ts=%s",
			evtIdx, mov.ID, mov.RepositoryID, mov.FromID, mov.ToID, evt.timestamp)

		repositoryMap, err := s.GetRepositoriesDetails(showDeletedCtx, tx)
		if err != nil {
			return fmt.Errorf("movement %s: failed loading repository map: %w", mov.ID, err)
		}
		// fromID may be uuid.Nil when the record omits it; fall back to the
		// repository's current parent in the DB.
		fromID := mov.FromID
		if fromID == uuid.Nil {
			repoRec, ok := repositoryMap[mov.RepositoryID]
			if !ok {
				return fmt.Errorf("movement %s: %w: %s", mov.ID, errRebuildRepositoryNotFound, mov.RepositoryID)
			}
			fromID = repoRec.ParentID
		}
		parentsMap := make(map[uuid.UUID]ent.Repository)
		if err = s.getRepositoriesParentsDetails(mov.RepositoryID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (repo): %w", mov.ID, err)
		}
		if err = s.getRepositoriesParentsDetails(mov.ToID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (to): %w", mov.ID, err)
		}
		if err = s.getRepositoriesParentsDetails(fromID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (from): %w", mov.ID, err)
		}
		stockMap := map[uuid.UUID]map[uuid.UUID]ent.Stock{}
		if repoIDs := rebuildMapKeys(parentsMap); len(repoIDs) > 0 {
			if stockMap, err = s.GetCurrentRepositoriesStock(ctx, tx, repoIDs); err != nil {
				return fmt.Errorf("movement %s: GetCurrentRepositoriesStock: %w", mov.ID, err)
			}
		}
		for itemID, itemRecord := range stockMap[mov.RepositoryID] {
			if err = s.simulateRepositoryStockMap(itemID, fromID, mov.ToID, -itemRecord.Quantity, stockMap, repositoryMap, false); err != nil {
				return fmt.Errorf("movement %s item %s: SimulateRepositoryStockMap (from): %w", mov.ID, itemID, err)
			}
			if err = s.simulateRepositoryStockMap(itemID, mov.ToID, fromID, itemRecord.Quantity, stockMap, repositoryMap, false); err != nil {
				return fmt.Errorf("movement %s item %s: SimulateRepositoryStockMap (to): %w", mov.ID, itemID, err)
			}
		}
		if err = rebuildInsertNestedStockMap(ctx, tx, tenantID, mov.ID, stockMap); err != nil {
			return fmt.Errorf("movement %s: rebuildInsertNestedStockMap: %w", mov.ID, err)
		}

	// ── EXECUTE REPO: mirrors ExecuteRepositoryMovement stock logic ───────────
	case rebuildExecuteRepo:
		mov := evt.repoMov
		s.debugf("EVENT[%d] EXECUTE_REPO mov=%s repo=%s from=%s to=%s ts=%s",
			evtIdx, mov.ID, mov.RepositoryID, mov.FromID, mov.ToID, evt.timestamp)

		fromID := mov.FromID
		if fromID == uuid.Nil {
			fromID = mov.RepositoryID
		}
		repoStockMap, err := s.GetCurrentRepositoriesStock(ctx, tx, []uuid.UUID{mov.RepositoryID})
		if err != nil {
			return fmt.Errorf("movement %s: GetCurrentRepositoriesStock: %w", mov.ID, err)
		}
		// Phase 4.3: load the FROM/TO ancestor closure once for the
		// per-item loop. Both per-item walks share the same endpoints
		// so they share the same closure; the items list is the union
		// of items currently held by the moving repository. The
		// rebuild path runs through showDeletedCtx so soft-deleted
		// ancestors stay visible during replay (executed-then-deleted
		// repos are needed for the parent walk).
		itemIDs := make([]uuid.UUID, 0, len(repoStockMap[mov.RepositoryID]))
		for itemID := range repoStockMap[mov.RepositoryID] {
			itemIDs = append(itemIDs, itemID)
		}
		repoMap, priorStocks, err := s.loadAncestorStocks(showDeletedCtx, tx, tenantID, []uuid.UUID{fromID, mov.ToID}, itemIDs, true)
		if err != nil {
			return fmt.Errorf("movement %s: loadAncestorStocks: %w", mov.ID, err)
		}
		// Compute the LCA of fromID and mov.ToID once from the
		// pre-loaded repoMap; both per-item walks share the same
		// endpoints so they share the same cutoff. Above the LCA the
		// +q from the TO-walk and -q from the FROM-walk cancel
		// (FINDINGS section 3.4); stopping there avoids no-op
		// snapshot rows.
		lcaID := lowestCommonAncestor(repoMap, fromID, mov.ToID)
		versions := newStockVersionTracker(priorStocks)
		creates := make([]*ent.StockCreate, 0)
		for itemID, itemRecord := range repoStockMap[mov.RepositoryID] {
			perItemMap := make(map[uuid.UUID]ent.Stock)
			if err = s.calculateRepositoryStockMap(itemID, fromID, lcaID, -itemRecord.Quantity, repoMap, priorStocks, perItemMap, false); err != nil {
				return fmt.Errorf("movement %s item %s: CalculateRepositoryStockMap (from): %w", mov.ID, itemID, err)
			}
			if err = s.calculateRepositoryStockMap(itemID, mov.ToID, lcaID, itemRecord.Quantity, repoMap, priorStocks, perItemMap, false); err != nil {
				return fmt.Errorf("movement %s item %s: CalculateRepositoryStockMap (to): %w", mov.ID, itemID, err)
			}
			for repoID, rec := range perItemMap {
				creates = append(creates, tx.Stock.Create().
					SetItemID(itemID).SetTenantID(tenantID).SetRepositoryID(repoID).
					SetQuantity(rec.Quantity).SetIncomingStock(rec.IncomingStock).SetOutgoingStock(rec.OutgoingStock).
					SetOwnQuantity(rec.OwnQuantity).SetOwnIncomingStock(rec.OwnIncomingStock).SetOwnOutgoingStock(rec.OwnOutgoingStock).
					SetMovementID(mov.ID).
					SetVersion(versions.nextFor(repoID, itemID)))
			}
		}
		if len(creates) > 0 {
			if err = stockCreateBulkChunked(ctx, tx, creates); err != nil {
				return fmt.Errorf("movement %s: CreateBulk execute-repo: %w", mov.ID, err)
			}
		}
		// Re-advance the repository's parent pointer so subsequent
		// CalculateRepositoryStockMap calls see the correct parent chain.
		if _, err = tx.Repository.UpdateOneID(mov.RepositoryID).SetParentID(mov.ToID).Save(showDeletedCtx); err != nil {
			return fmt.Errorf("movement %s: advancing repository parent to %s: %w", mov.ID, mov.ToID, err)
		}

	// ── DELETE REPO: mirrors DeleteRepositoryMovement stock logic ─────────────
	case rebuildDeleteRepo:
		mov := evt.repoMov
		s.debugf("EVENT[%d] DELETE_REPO mov=%s repo=%s from=%s to=%s ts=%s",
			evtIdx, mov.ID, mov.RepositoryID, mov.FromID, mov.ToID, evt.timestamp)

		repositoryMap, err := s.GetRepositoriesDetails(showDeletedCtx, tx)
		if err != nil {
			return fmt.Errorf("movement %s: failed loading repository map: %w", mov.ID, err)
		}
		fromID := mov.FromID
		if fromID == uuid.Nil {
			repoRec, ok := repositoryMap[mov.RepositoryID]
			if !ok {
				return fmt.Errorf("movement %s: %w: %s", mov.ID, errRebuildRepositoryNotFound, mov.RepositoryID)
			}
			fromID = repoRec.ParentID
		}
		parentsMap := make(map[uuid.UUID]ent.Repository)
		if err = s.getRepositoriesParentsDetails(mov.RepositoryID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (repo): %w", mov.ID, err)
		}
		if err = s.getRepositoriesParentsDetails(mov.ToID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (to): %w", mov.ID, err)
		}
		stockMap := map[uuid.UUID]map[uuid.UUID]ent.Stock{}
		if repoIDs := rebuildMapKeys(parentsMap); len(repoIDs) > 0 {
			if stockMap, err = s.GetCurrentRepositoriesStock(ctx, tx, repoIDs); err != nil {
				return fmt.Errorf("movement %s: GetCurrentRepositoriesStock: %w", mov.ID, err)
			}
		}
		// Read the per-item quantities from the stock snapshot written by the
		// PENDING_REPO event for this movement (tagged with the same movementID).
		originalRows, err := tx.Stock.Query().
			Where(entstock.MovementID(mov.ID), entstock.RepositoryID(mov.RepositoryID)).
			AllPages(ctx, mixin.Limit)
		if err != nil {
			return fmt.Errorf("movement %s: reading original stock snapshot: %w", mov.ID, err)
		}
		for _, row := range originalRows {
			if err = s.simulateRepositoryStockMapWithMode(row.ItemID, fromID, mov.ToID, -row.Quantity, stockMap, repositoryMap, false, true); err != nil {
				return fmt.Errorf("movement %s item %s: SimulateRepositoryStockMapWithMode (from): %w", mov.ID, row.ItemID, err)
			}
			if err = s.simulateRepositoryStockMapWithMode(row.ItemID, mov.ToID, fromID, row.Quantity, stockMap, repositoryMap, false, true); err != nil {
				return fmt.Errorf("movement %s item %s: SimulateRepositoryStockMapWithMode (to): %w", mov.ID, row.ItemID, err)
			}
		}
		if err = rebuildInsertNestedStockMap(ctx, tx, tenantID, mov.ID, stockMap); err != nil {
			return fmt.Errorf("movement %s: rebuildInsertNestedStockMap: %w", mov.ID, err)
		}
	}

	return nil
}

// rebuildMapKeys returns the keys of a map[uuid.UUID]T as a slice.
func rebuildMapKeys[T any](m map[uuid.UUID]T) []uuid.UUID {
	keys := make([]uuid.UUID, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// rebuildInsertNestedStockMap bulk-inserts all repo×item combinations from a
// nested stockMap (repoID → itemID → Stock). Versions are drawn from the
// nested map's existing Version field (loaded via
// GetCurrentRepositoriesStock) — the rebuild path replays one event at
// a time so a single seed-from-nested pass is sufficient.
//
// On rich tenants the (repo × item) closure for a single movement can
// exceed PostgreSQL's 65,535-parameter wire-protocol limit when emitted
// as one INSERT (pyck-ai/pyck#1227); the actual write is delegated to
// stockCreateBulkChunked which splits the inserts into safe batches.
func rebuildInsertNestedStockMap(ctx context.Context, tx *ent.Tx, tenantID, movementID uuid.UUID, stockMap map[uuid.UUID]map[uuid.UUID]ent.Stock) error {
	versions := newStockVersionTracker(nil)
	versions.seedFromNested(stockMap)
	creates := make([]*ent.StockCreate, 0)
	for repoID, items := range stockMap {
		for itemID, rec := range items {
			creates = append(creates, tx.Stock.Create().
				SetItemID(itemID).SetTenantID(tenantID).SetRepositoryID(repoID).
				SetQuantity(rec.Quantity).SetIncomingStock(rec.IncomingStock).SetOutgoingStock(rec.OutgoingStock).
				SetOwnQuantity(rec.OwnQuantity).SetOwnIncomingStock(rec.OwnIncomingStock).SetOwnOutgoingStock(rec.OwnOutgoingStock).
				SetMovementID(movementID).
				SetVersion(versions.nextFor(repoID, itemID)))
		}
	}
	if err := stockCreateBulkChunked(ctx, tx, creates); err != nil {
		return fmt.Errorf("failed inserting stock rows: %w", err)
	}
	return nil
}

// rebuildEventID returns the movement UUID embedded in a rebuildEvent.
func rebuildEventID(e rebuildEvent) uuid.UUID {
	if e.itemMov != nil {
		return e.itemMov.ID
	}
	if e.repoMov != nil {
		return e.repoMov.ID
	}
	return uuid.Nil
}

// rebuildCmpUUID returns negative/zero/positive like bytes.Compare on UUID bytes.
func rebuildCmpUUID(a, b uuid.UUID) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}
