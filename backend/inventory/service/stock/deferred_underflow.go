package stock

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
)

// deferredUnderflowKey is the unexported context key carrying the
// "skip per-call underflow rejection" flag. Phase 9 collection-create
// callers set the flag for the duration of the loop they drive over
// CreateItemMovement; after the loop they invoke
// consistencyCheckSourceRows to validate the *final* state of every
// touched source repository in one shot. The flag is opt-in: ctx
// values not carrying it produce IsDeferredUnderflow == false and the
// direct create path keeps its historical per-call rejection.
type deferredUnderflowKey struct{}

// WithDeferredUnderflow returns ctx annotated with the
// deferred-underflow flag. CreateItemMovement consults the flag via
// IsDeferredUnderflow and, when set, skips both the proc dispatch
// (the plpgsql proc raises STOCK_INSUFFICIENT internally and adding
// a "skip check" parameter would couple it to the collection
// plan-ahead semantics) and the Go path's per-call FROM-availability
// rejection. The bookkeeping (movement INSERT, stock-map fan-out,
// incoming/outgoing/quantity updates) runs unchanged so the chain of
// movements builds up the same intermediate state it would under per-
// call validation; the *error* return is the only thing deferred. The
// caller is responsible for running consistencyCheckSourceRows on the
// union of source repos and items it touched to catch any negative
// final availability before the surrounding tx commits.
func WithDeferredUnderflow(ctx context.Context) context.Context {
	return context.WithValue(ctx, deferredUnderflowKey{}, true)
}

// IsDeferredUnderflow reports whether WithDeferredUnderflow has been
// applied to ctx. CreateItemMovement and any future direct-create
// underflow gate consult this helper; collection-tier callers set the
// flag for the duration of their loop. When the flag is absent (the
// default for every direct-create caller outside Phase 9 collection
// resolvers) behavior is unchanged.
func IsDeferredUnderflow(ctx context.Context) bool {
	v, _ := ctx.Value(deferredUnderflowKey{}).(bool)
	return v
}

// consistencyCheckSourceRows validates that, given the latest stock
// rows visible to tx, every (repo, item) combination in the supplied
// source closures has non-negative effective availability. It is the
// post-loop counterpart to WithDeferredUnderflow: collection-tier
// callers run this once after the per-position direct creates so the
// final state is checked even though the per-call rejection was
// suppressed. Returning a non-nil error lets the surrounding gqltx
// middleware roll back the whole tx, so a chain that is internally
// inconsistent (sources can never satisfy targets) is caught before
// commit just as the per-call check would have caught it on the
// first violating call.
//
// Effective availability is defined the same way the Go-create path
// computes its FROM guard at line 514 of impl.go: Quantity +
// IncomingStock - OutgoingStock. A virtual source row never appears
// here because virtual repos are clamped to zero by
// applyRepositoryStockDelta and have no meaningful availability; the
// (repo, item) pairs the caller passes in are by construction
// non-virtual sources.
//
// repoIDs and itemIDs together describe the closure to inspect: the
// helper loads ancestor stocks for the union of repoIDs (so any
// parent rows updated by the chain are read back) and narrows the
// returned stock rows to itemIDs. Empty repoIDs is a no-op; empty
// itemIDs widens to "every item with a row at one of the repos",
// matching loadAncestorStocks' semantics.
func (s *service) consistencyCheckSourceRows(
	ctx context.Context,
	tx *ent.Tx,
	tenantID uuid.UUID,
	repoIDs []uuid.UUID,
	itemIDs []uuid.UUID,
) error {
	if len(repoIDs) == 0 {
		return nil
	}

	_, stocks, err := s.loadAncestorStocks(ctx, tx, tenantID, repoIDs, itemIDs, false)
	if err != nil {
		return fmt.Errorf("consistencyCheckSourceRows: load ancestor stocks: %w", err)
	}

	// Restrict the check to the source repositories the caller passed
	// in. loadAncestorStocks expands the closure to include parents
	// (every ancestor up to the root or the LCA), but parents may
	// legitimately net to zero or negative on intermediate rows when a
	// chain has not yet propagated all updates — only the leaf source
	// rows are guaranteed to settle to a non-negative final state.
	sourceSet := make(map[uuid.UUID]struct{}, len(repoIDs))
	for _, id := range repoIDs {
		sourceSet[id] = struct{}{}
	}

	for key, rec := range stocks {
		if _, ok := sourceSet[key.RepositoryID]; !ok {
			continue
		}
		effective := rec.Quantity + rec.IncomingStock - rec.OutgoingStock
		if effective < 0 {
			return fmt.Errorf("%w: repository=%s item=%s effective=%d",
				errStockUnderflow, key.RepositoryID, key.ItemID, effective)
		}
	}
	return nil
}
