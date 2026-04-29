package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/std"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/itemmovement"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/predicate"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/repositorymovement"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

var (
	ErrOutOfOrderExecution          = errors.New("unable to execute the current movement. First please execute previous movements in the collection")
	ErrStockUnderflow               = errors.New("stock underflow")
	ErrRebuildRepoNotFoundInRepoMap = errors.New("repository movement references repository which is not found in repoMap")
	ErrRebuildRepositoryNotFound    = errors.New("repository not found during rebuild event replay")
)

type InventoryStockService struct {
	// DebugLog, when non-nil, receives structured trace lines for every stock
	// mutation (both real-ops and rebuild paths). Used exclusively by tests to
	// compare the two execution paths.
	DebugLog io.Writer
}

func NewInventoryStockService() (*InventoryStockService, error) {
	return &InventoryStockService{}, nil
}

// debugf writes a formatted line to DebugLog when it is set.
func (s *InventoryStockService) debugf(format string, args ...any) {
	if s.DebugLog != nil {
		fmt.Fprintf(s.DebugLog, format+"\n", args...)
	}
}

func (e *InventoryStockService) Close() {
}

func (s *InventoryStockService) CalculateRepositoryStockMap(ctx context.Context, tx *ent.Tx, itemID, repositoryID uuid.UUID, quantity int64, stockMap map[uuid.UUID]ent.Stock, ownStock bool) error {
	if err := s.applyRepositoryStockDelta(ctx, tx, itemID, repositoryID, quantity, stockMap, ownStock); err != nil {
		return err
	}
	return s.ValidateStockMapNoUnderflow(stockMap)
}

// ApplyItemMovementStockDelta applies the FROM-walk and TO-walk for an item
// movement and validates the resulting stockMap once both walks have run. This
// is the correct shape for the executor: validating per-walk over-rejects the
// case where FROM and TO share a common ancestor whose net delta is zero
func (s *InventoryStockService) ApplyItemMovementStockDelta(ctx context.Context, tx *ent.Tx, itemID, fromID, toID uuid.UUID, quantity int64, stockMap map[uuid.UUID]ent.Stock, ownStock bool) error {
	if err := s.applyRepositoryStockDelta(ctx, tx, itemID, fromID, -quantity, stockMap, ownStock); err != nil {
		return err
	}
	if err := s.applyRepositoryStockDelta(ctx, tx, itemID, toID, quantity, stockMap, ownStock); err != nil {
		return err
	}
	return s.ValidateStockMapNoUnderflow(stockMap)
}

// ValidateStockMapNoUnderflow returns ErrStockUnderflow if any non-virtual
// repository in stockMap has a negative Quantity. Virtual repos are clamped
// to zero by applyRepositoryStockDelta, so a single Quantity < 0 check is
// sufficient.
func (s *InventoryStockService) ValidateStockMapNoUnderflow(stockMap map[uuid.UUID]ent.Stock) error {
	for repoID, rec := range stockMap {
		if rec.Quantity < 0 {
			return fmt.Errorf("%w: repository=%s quantity would be %d", ErrStockUnderflow, repoID, rec.Quantity)
		}
	}
	return nil
}

// applyRepositoryStockDelta walks the parent chain of repositoryID and
// accumulates the delta into stockMap without validating underflow. Callers
// must run ValidateStockMapNoUnderflow after all desired walks have been
// applied; transient negative quantities at intermediate ancestors are
// expected when FROM and TO share a common ancestor.
func (s *InventoryStockService) applyRepositoryStockDelta(ctx context.Context, tx *ent.Tx, itemID, repositoryID uuid.UUID, quantity int64, stockMap map[uuid.UUID]ent.Stock, ownStock bool) error {
	stockRecordQuantity := int64(0)
	incomingStockRecordQuantity := int64(0)
	outgoingStockRecordQuantity := int64(0)

	ownStockRecordQuantity := int64(0)
	ownIncomingStockRecordQuantity := int64(0)
	ownOutgoingStockRecordQuantity := int64(0)

	// Retrieve current values for repository
	repo, err := tx.Repository.Get(ctx, repositoryID)
	if err != nil {
		return fmt.Errorf("repository not found")
	}

	var stockRecord *ent.Stock

	// Check is stockMap already contains repositoryID; if not read the last stock from db
	if record, found := stockMap[repositoryID]; found {
		stockRecordQuantity = record.Quantity
		incomingStockRecordQuantity = record.IncomingStock
		outgoingStockRecordQuantity = record.OutgoingStock
		ownStockRecordQuantity = record.OwnQuantity
		ownIncomingStockRecordQuantity = record.OwnIncomingStock
		ownOutgoingStockRecordQuantity = record.OwnOutgoingStock
	} else {
		where := predicate.Stock(func(s *sql.Selector) {
			s.Where(sql.EQ(stock.RepositoryColumn, repositoryID))
			s.Where(sql.EQ(stock.ItemColumn, itemID))
		})

		stockRecord, err = tx.Stock.Query().
			Where(where).
			Order(ent.Desc(stock.FieldCreatedAt)).
			First(ctx)

		// "stock not found" err means that stock for item-repository combination is 0
		if err != nil && !ent.IsNotFound(err) {
			return fmt.Errorf("failed reading stock: %w", err)
		}

		if err == nil {
			stockRecordQuantity = stockRecord.Quantity
			incomingStockRecordQuantity = stockRecord.IncomingStock
			outgoingStockRecordQuantity = stockRecord.OutgoingStock
			ownStockRecordQuantity = stockRecord.OwnQuantity
			ownIncomingStockRecordQuantity = stockRecord.OwnIncomingStock
			ownOutgoingStockRecordQuantity = stockRecord.OwnOutgoingStock
		}
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

	if repo.ParentID == uuid.Nil {
		return nil
	}

	return s.applyRepositoryStockDelta(ctx, tx, itemID, repo.ParentID, quantity, stockMap, false)
}

func (s *InventoryStockService) SimulateRepositoryStockMap(itemID, repositoryID, sourceRepositoryID uuid.UUID, quantity int64, stockMap map[uuid.UUID]map[uuid.UUID]ent.Stock, repoMap map[uuid.UUID]ent.Repository, ownStock bool) error {
	return s.SimulateRepositoryStockMapWithMode(itemID, repositoryID, sourceRepositoryID, quantity, stockMap, repoMap, ownStock, false)
}

func (s *InventoryStockService) SimulateRepositoryStockMapWithMode(itemID, repositoryID, sourceRepositoryID uuid.UUID, quantity int64, stockMap map[uuid.UUID]map[uuid.UUID]ent.Stock, repoMap map[uuid.UUID]ent.Repository, ownStock bool, subtract bool) error {
	if stockMap[repositoryID] == nil {
		stockMap[repositoryID] = make(map[uuid.UUID]ent.Stock)
	}

	updatedRecord := stockMap[repositoryID][itemID]

	if ownStock {
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

	// Avoid tracking the internal movements within repository
	if !s.CheckIfRepositoryIsChildOfRepository(sourceRepositoryID, repositoryID, repoMap) {
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

	if repoMap[repositoryID].ParentID == uuid.Nil {
		return nil
	}

	return s.SimulateRepositoryStockMapWithMode(itemID, repoMap[repositoryID].ParentID, sourceRepositoryID, quantity, stockMap, repoMap, false, subtract)
}

func (s *InventoryStockService) GetCurrentRepositoriesStock(ctx context.Context, tx *ent.Tx, repositoryIDs []uuid.UUID) (map[uuid.UUID]map[uuid.UUID]ent.Stock, error) {
	repoPred := stock.RepositoryIDIn(repositoryIDs...)

	records, err := tx.Stock.Query().
		Where(repoPred).
		DistinctOnExists(
			[]string{stock.FieldRepositoryID, stock.FieldItemID},
			stock.FieldCreatedAt,
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

func (s *InventoryStockService) GetRepositoriesDetails(ctx context.Context, tx *ent.Tx) (map[uuid.UUID]ent.Repository, error) {
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

func (s *InventoryStockService) GetRepositoriesParentsDetails(repositoryID uuid.UUID, reposMap, parentsMap map[uuid.UUID]ent.Repository) error {
	for _, v := range reposMap {
		if v.ID == repositoryID {
			parentsMap[repositoryID] = reposMap[repositoryID]

			if reposMap[repositoryID].ParentID == uuid.Nil {
				return nil
			}
			return s.GetRepositoriesParentsDetails(v.ParentID, reposMap, parentsMap)
		}
	}

	return nil
}

func (s *InventoryStockService) CheckIfRepositoryIsChildOfRepository(childRepositoryID, parentRepositoryID uuid.UUID, reposMap map[uuid.UUID]ent.Repository) bool {
	if childRepositoryID == parentRepositoryID {
		return true
	}

	for _, v := range reposMap {
		if v.ID == childRepositoryID {
			if reposMap[childRepositoryID].ParentID == uuid.Nil {
				return false
			}

			if reposMap[childRepositoryID].ParentID == parentRepositoryID {
				return true
			}

			return s.CheckIfRepositoryIsChildOfRepository(v.ParentID, parentRepositoryID, reposMap)
		}
	}

	return false
}

func (s *InventoryStockService) CheckExecuteNextMovementByPosition(ctx context.Context, tx *ent.Tx, collectionID uuid.UUID, position int) error {
	// Query non-deleted item movements in this collection.
	itemMovements, err := tx.ItemMovement.Query().
		Where(
			itemmovement.CollectionID(collectionID),
			itemmovement.DeletedAtIsNil(),
		).
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query item movements: %w", err)
	}

	// Query non-deleted repository movements in this collection.
	repoMovements, err := tx.RepositoryMovement.Query().
		Where(
			repositorymovement.CollectionID(collectionID),
			repositorymovement.DeletedAtIsNil(),
		).
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query repository movements: %w", err)
	}

	// Check that all movements at earlier positions are already executed.
	for _, v := range itemMovements {
		if position > v.Position && !v.Executed {
			return fmt.Errorf("%w: %s", ErrOutOfOrderExecution, v.ID)
		}
	}
	for _, v := range repoMovements {
		if position > v.Position && !v.Executed {
			return fmt.Errorf("%w: %s", ErrOutOfOrderExecution, v.ID)
		}
	}

	return nil
}

func (s *InventoryStockService) CreateInventoryCollectionItemMovement(ctx context.Context, tx *ent.Tx, input ent.CreateItemMovementInput, tenantID uuid.UUID, stockMap map[uuid.UUID]map[uuid.UUID]ent.Stock, repoMap map[uuid.UUID]ent.Repository) (*ent.ItemMovement, error) {
	if !repoMap[input.FromID].VirtualRepo {
		reservedQuantity, err := s.CalculateReservedStockByCollectionId(ctx, tx, input.FromID, input.ItemID, input.CollectionID)
		if err != nil {
			return nil, fmt.Errorf("failed calculating reserved stock: %w", err)
		}

		if stockMap[input.FromID][input.ItemID].Quantity+stockMap[input.FromID][input.ItemID].IncomingStock-reservedQuantity < input.Quantity {
			return nil, errors.New("insufficient stock")
		}
	}

	movement, err := tx.ItemMovement.
		Create().
		SetInput(input).
		SetTenantID(tenantID).
		SetExecuted(false).
		Save(ctx)
	if err != nil {
		return nil, err
	}

	// Calculate stockMap for parents of the 'from' repository
	err = s.SimulateRepositoryStockMap(input.ItemID, input.FromID, input.ToID, -1*input.Quantity, stockMap, repoMap, true)
	if err != nil {
		return nil, fmt.Errorf("failed calculating stock map: %w", err)
	}

	// Calculate stockMap for parents of the 'to' repository
	err = s.SimulateRepositoryStockMap(input.ItemID, input.ToID, input.FromID, input.Quantity, stockMap, repoMap, true)
	if err != nil {
		return nil, fmt.Errorf("failed calculating stock map: %w", err)
	}

	return movement, err
}

func (s *InventoryStockService) CalculateReservedStockByCollectionId(ctx context.Context, tx *ent.Tx, repositoryID, itemID uuid.UUID, collectionID *uuid.UUID) (int64, error) {
	user := authn.ForContext(ctx)

	if collectionID == nil {
		collectionID = &uuid.Nil
	}

	whereRepositoryMovementsReserved := predicate.RepositoryMovement(func(s *sql.Selector) {
		s.Where(sql.EQ(repositorymovement.FieldExecuted, false))
		s.Where(sql.Or(sql.EQ(repositorymovement.FieldRepositoryID, repositoryID), sql.EQ(repositorymovement.FieldFromID, repositoryID)))
		s.Where(sql.Or(sql.EQ(repositorymovement.FieldCollectionID, uuid.Nil), sql.NEQ(repositorymovement.FieldCollectionID, collectionID)))
	})
	repositoryMovementsReserved, err := tx.RepositoryMovement.Query().
		Where(whereRepositoryMovementsReserved).
		Where(repositorymovement.TenantID(user.TenantID)).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed calculating reserved repository movements: %w", err)
	}

	reservedChildren := make([]uuid.UUID, 0)
	for _, v := range repositoryMovementsReserved {
		if v.RepositoryID == repositoryID {
			return 0, fmt.Errorf("cannot move items from reserved repository")
		}

		if v.FromID == repositoryID {
			reservedChildren = append(reservedChildren, v.RepositoryID)
		}
	}

	reservedQuantity := int64(0)
	if len(reservedChildren) > 0 {
		stockMap, err := s.GetCurrentRepositoriesStock(ctx, tx, reservedChildren)
		if err != nil {
			return 0, err
		}

		for _, v := range repositoryMovementsReserved {
			reservedQuantity += stockMap[v.RepositoryID][itemID].OutgoingStock
		}
	}

	whereItemMovementsReserved := predicate.ItemMovement(func(s *sql.Selector) {
		s.Where(sql.EQ(itemmovement.FieldItemID, itemID))
		s.Where(sql.EQ(itemmovement.FieldExecuted, false))
		s.Where(sql.Or(sql.EQ(itemmovement.FieldCollectionID, uuid.Nil), sql.NEQ(itemmovement.FieldCollectionID, collectionID)))
		s.Where(sql.EQ(itemmovement.FromColumn, repositoryID))
	})

	itemMovementsReserved, err := tx.ItemMovement.Query().
		Where(whereItemMovementsReserved).
		Where(itemmovement.TenantID(user.TenantID)).
		All(ctx)
	if err != nil {
		return 0, err
	}

	for _, v := range itemMovementsReserved {
		reservedQuantity += v.Quantity
	}

	return reservedQuantity, nil
}

func (s *InventoryStockService) InsertStockMap(ctx context.Context, tx *ent.Tx, itemID, tenantID, movementID uuid.UUID, stockMap map[uuid.UUID]map[uuid.UUID]ent.Stock, repositoryMap map[uuid.UUID]ent.Repository) error {
	stocksToCreate := []*ent.StockCreate{}
	for repo := range stockMap {
		quantity := int64(0)
		incomingQuantity := int64(0)
		outgoingQuantity := int64(0)
		ownQuantity := int64(0)
		ownIncomingQuantity := int64(0)
		ownOutgoingQuantity := int64(0)
		if stockMap[repo] != nil {
			quantity = stockMap[repo][itemID].Quantity
			incomingQuantity = stockMap[repo][itemID].IncomingStock
			outgoingQuantity = stockMap[repo][itemID].OutgoingStock
			ownQuantity = stockMap[repo][itemID].OwnQuantity
			ownIncomingQuantity = stockMap[repo][itemID].OwnIncomingStock
			ownOutgoingQuantity = stockMap[repo][itemID].OwnOutgoingStock
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
			SetMovementID(movementID)
		stocksToCreate = append(stocksToCreate, newStock)
	}
	_, err := tx.Stock.CreateBulk(stocksToCreate...).Save(ctx)
	if err != nil {
		return fmt.Errorf("failed inserting stocks: %w", err)
	}

	return nil
}

func (s *InventoryStockService) Contains(list []uuid.UUID, id uuid.UUID) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
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
func (s *InventoryStockService) RebuildStockTable(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID) error {
	// showDeletedCtx bypasses the HistoryMixin soft-delete filter.
	showDeletedCtx := feature.Context(ctx, feature.FEATURE_SHOW_DELETED)

	// Step 1 – delete all existing stock rows for this tenant.
	if _, err := tx.Stock.Delete().Where(stock.TenantID(tenantID)).Exec(ctx); err != nil {
		return fmt.Errorf("failed to delete stock records: %w", err)
	}

	// Step 2 – load ALL repository movements (including soft-deleted).
	// Paginate because LimitMixin enforces a hard cap of 200 rows per query.
	const rebuildPageSize = 200

	var allRepoMovs []*ent.RepositoryMovement
	for offset := 0; ; offset += rebuildPageSize {
		page, pageErr := tx.RepositoryMovement.Query().
			Where(repositorymovement.TenantID(tenantID)).
			Order(repositorymovement.ByCreatedAt(), repositorymovement.ByID()).
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
			return fmt.Errorf("movement %s: %w: %s", mov.ID, ErrRebuildRepoNotFoundInRepoMap, mov.RepositoryID)
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
			Where(itemmovement.TenantID(tenantID)).
			Order(itemmovement.ByCreatedAt(), itemmovement.ByID()).
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
func (s *InventoryStockService) replayRebuildEvent(
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
		if err = s.GetRepositoriesParentsDetails(mov.FromID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (from): %w", mov.ID, err)
		}
		if err = s.GetRepositoriesParentsDetails(mov.ToID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (to): %w", mov.ID, err)
		}
		stockMap := map[uuid.UUID]map[uuid.UUID]ent.Stock{}
		if repoIDs := rebuildMapKeys(parentsMap); len(repoIDs) > 0 {
			if stockMap, err = s.GetCurrentRepositoriesStock(ctx, tx, repoIDs); err != nil {
				return fmt.Errorf("movement %s: GetCurrentRepositoriesStock: %w", mov.ID, err)
			}
		}
		if err = s.SimulateRepositoryStockMap(mov.ItemID, mov.FromID, mov.ToID, -mov.Quantity, stockMap, repositoryMap, true); err != nil {
			return fmt.Errorf("movement %s: SimulateRepositoryStockMap (from): %w", mov.ID, err)
		}
		if err = s.SimulateRepositoryStockMap(mov.ItemID, mov.ToID, mov.FromID, mov.Quantity, stockMap, repositoryMap, true); err != nil {
			return fmt.Errorf("movement %s: SimulateRepositoryStockMap (to): %w", mov.ID, err)
		}
		if err = s.InsertStockMap(ctx, tx, mov.ItemID, tenantID, mov.ID, stockMap, repositoryMap); err != nil {
			return fmt.Errorf("movement %s: InsertStockMap: %w", mov.ID, err)
		}

	// ── EXECUTE ITEM: mirrors ExecuteItemMovement stock logic ────────────────
	case rebuildExecuteItem:
		mov := evt.itemMov
		s.debugf("EVENT[%d] EXECUTE_ITEM mov=%s item=%s from=%s to=%s qty=%d ts=%s",
			evtIdx, mov.ID, mov.ItemID, mov.FromID, mov.ToID, mov.Quantity, evt.timestamp)

		stockMap := make(map[uuid.UUID]ent.Stock)
		if err := s.ApplyItemMovementStockDelta(ctx, tx, mov.ItemID, mov.FromID, mov.ToID, mov.Quantity, stockMap, true); err != nil {
			return fmt.Errorf("movement %s: ApplyItemMovementStockDelta: %w", mov.ID, err)
		}
		creates := make([]*ent.StockCreate, 0, len(stockMap))
		for repoID, rec := range stockMap {
			creates = append(creates, tx.Stock.Create().
				SetItemID(mov.ItemID).SetTenantID(tenantID).SetRepositoryID(repoID).
				SetQuantity(rec.Quantity).SetIncomingStock(rec.IncomingStock).SetOutgoingStock(rec.OutgoingStock).
				SetOwnQuantity(rec.OwnQuantity).SetOwnIncomingStock(rec.OwnIncomingStock).SetOwnOutgoingStock(rec.OwnOutgoingStock).
				SetMovementID(mov.ID))
		}
		if len(creates) > 0 {
			if _, err := tx.Stock.CreateBulk(creates...).Save(ctx); err != nil {
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
		if err = s.GetRepositoriesParentsDetails(mov.FromID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (from): %w", mov.ID, err)
		}
		if err = s.GetRepositoriesParentsDetails(mov.ToID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (to): %w", mov.ID, err)
		}
		stockMap := map[uuid.UUID]map[uuid.UUID]ent.Stock{}
		if repoIDs := rebuildMapKeys(parentsMap); len(repoIDs) > 0 {
			if stockMap, err = s.GetCurrentRepositoriesStock(ctx, tx, repoIDs); err != nil {
				return fmt.Errorf("movement %s: GetCurrentRepositoriesStock: %w", mov.ID, err)
			}
		}
		if err = s.SimulateRepositoryStockMapWithMode(mov.ItemID, mov.FromID, mov.ToID, -mov.Quantity, stockMap, repositoryMap, true, true); err != nil {
			return fmt.Errorf("movement %s: SimulateRepositoryStockMapWithMode (from): %w", mov.ID, err)
		}
		if err = s.SimulateRepositoryStockMapWithMode(mov.ItemID, mov.ToID, mov.FromID, mov.Quantity, stockMap, repositoryMap, true, true); err != nil {
			return fmt.Errorf("movement %s: SimulateRepositoryStockMapWithMode (to): %w", mov.ID, err)
		}
		if err = s.InsertStockMap(ctx, tx, mov.ItemID, tenantID, mov.ID, stockMap, repositoryMap); err != nil {
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
				return fmt.Errorf("movement %s: %w: %s", mov.ID, ErrRebuildRepositoryNotFound, mov.RepositoryID)
			}
			fromID = repoRec.ParentID
		}
		parentsMap := make(map[uuid.UUID]ent.Repository)
		if err = s.GetRepositoriesParentsDetails(mov.RepositoryID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (repo): %w", mov.ID, err)
		}
		if err = s.GetRepositoriesParentsDetails(mov.ToID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (to): %w", mov.ID, err)
		}
		if err = s.GetRepositoriesParentsDetails(fromID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (from): %w", mov.ID, err)
		}
		stockMap := map[uuid.UUID]map[uuid.UUID]ent.Stock{}
		if repoIDs := rebuildMapKeys(parentsMap); len(repoIDs) > 0 {
			if stockMap, err = s.GetCurrentRepositoriesStock(ctx, tx, repoIDs); err != nil {
				return fmt.Errorf("movement %s: GetCurrentRepositoriesStock: %w", mov.ID, err)
			}
		}
		for itemID, itemRecord := range stockMap[mov.RepositoryID] {
			if err = s.SimulateRepositoryStockMap(itemID, fromID, mov.ToID, -itemRecord.Quantity, stockMap, repositoryMap, false); err != nil {
				return fmt.Errorf("movement %s item %s: SimulateRepositoryStockMap (from): %w", mov.ID, itemID, err)
			}
			if err = s.SimulateRepositoryStockMap(itemID, mov.ToID, fromID, itemRecord.Quantity, stockMap, repositoryMap, false); err != nil {
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
		creates := make([]*ent.StockCreate, 0)
		for itemID, itemRecord := range repoStockMap[mov.RepositoryID] {
			perItemMap := make(map[uuid.UUID]ent.Stock)
			if err = s.CalculateRepositoryStockMap(ctx, tx, itemID, fromID, -itemRecord.Quantity, perItemMap, false); err != nil {
				return fmt.Errorf("movement %s item %s: CalculateRepositoryStockMap (from): %w", mov.ID, itemID, err)
			}
			if err = s.CalculateRepositoryStockMap(ctx, tx, itemID, mov.ToID, itemRecord.Quantity, perItemMap, false); err != nil {
				return fmt.Errorf("movement %s item %s: CalculateRepositoryStockMap (to): %w", mov.ID, itemID, err)
			}
			for repoID, rec := range perItemMap {
				creates = append(creates, tx.Stock.Create().
					SetItemID(itemID).SetTenantID(tenantID).SetRepositoryID(repoID).
					SetQuantity(rec.Quantity).SetIncomingStock(rec.IncomingStock).SetOutgoingStock(rec.OutgoingStock).
					SetOwnQuantity(rec.OwnQuantity).SetOwnIncomingStock(rec.OwnIncomingStock).SetOwnOutgoingStock(rec.OwnOutgoingStock).
					SetMovementID(mov.ID))
			}
		}
		if len(creates) > 0 {
			if _, err = tx.Stock.CreateBulk(creates...).Save(ctx); err != nil {
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
				return fmt.Errorf("movement %s: %w: %s", mov.ID, ErrRebuildRepositoryNotFound, mov.RepositoryID)
			}
			fromID = repoRec.ParentID
		}
		parentsMap := make(map[uuid.UUID]ent.Repository)
		if err = s.GetRepositoriesParentsDetails(mov.RepositoryID, repositoryMap, parentsMap); err != nil {
			return fmt.Errorf("movement %s: GetRepositoriesParentsDetails (repo): %w", mov.ID, err)
		}
		if err = s.GetRepositoriesParentsDetails(mov.ToID, repositoryMap, parentsMap); err != nil {
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
			Where(stock.MovementID(mov.ID), stock.RepositoryID(mov.RepositoryID)).
			All(ctx)
		if err != nil {
			return fmt.Errorf("movement %s: reading original stock snapshot: %w", mov.ID, err)
		}
		for _, row := range originalRows {
			if err = s.SimulateRepositoryStockMapWithMode(row.ItemID, fromID, mov.ToID, -row.Quantity, stockMap, repositoryMap, false, true); err != nil {
				return fmt.Errorf("movement %s item %s: SimulateRepositoryStockMapWithMode (from): %w", mov.ID, row.ItemID, err)
			}
			if err = s.SimulateRepositoryStockMapWithMode(row.ItemID, mov.ToID, fromID, row.Quantity, stockMap, repositoryMap, false, true); err != nil {
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
// nested stockMap (repoID → itemID → Stock) in a single CreateBulk call.
func rebuildInsertNestedStockMap(ctx context.Context, tx *ent.Tx, tenantID, movementID uuid.UUID, stockMap map[uuid.UUID]map[uuid.UUID]ent.Stock) error {
	creates := make([]*ent.StockCreate, 0)
	for repoID, items := range stockMap {
		for itemID, rec := range items {
			creates = append(creates, tx.Stock.Create().
				SetItemID(itemID).SetTenantID(tenantID).SetRepositoryID(repoID).
				SetQuantity(rec.Quantity).SetIncomingStock(rec.IncomingStock).SetOutgoingStock(rec.OutgoingStock).
				SetOwnQuantity(rec.OwnQuantity).SetOwnIncomingStock(rec.OwnIncomingStock).SetOwnOutgoingStock(rec.OwnOutgoingStock).
				SetMovementID(movementID))
		}
	}
	if len(creates) == 0 {
		return nil
	}
	if _, err := tx.Stock.CreateBulk(creates...).Save(ctx); err != nil {
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
