// Package stock owns the inventory stock-mutation domain logic.
//
// Service is the interface resolvers and main.go consume. Concrete
// state is package-private; construct an instance via New(). The
// exported surface was tightened in Phase 2.9.4: the interface now
// lists only the per-resolver entry points (Create/Execute/Delete
// item, repository, and collection movements), the few direct
// helpers resolvers still call (GetRepositoriesDetails,
// GetCurrentRepositoriesStock, RebuildStockTable), the two
// lifecycle/test methods (Close, SetDebugLog), New, and the input
// DTOs resolvers consume. Internal helpers and error sentinels live
// here lowercase.
package stock

import (
	"context"
	"errors"
	"io"

	entgo "entgo.io/ent"
	"github.com/google/uuid"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
)

// errOutOfOrderExecution is returned when a movement is executed before an
// earlier-positioned, non-executed movement in the same collection.
var errOutOfOrderExecution = errors.New("unable to execute the current movement. First please execute previous movements in the collection")

// errInsufficientStock is returned when the FROM repository does not hold
// enough stock to satisfy a requested item movement quantity.
var errInsufficientStock = errors.New("insufficient stock")

// errVirtualRepoMovement is returned when an item movement is requested
// between two virtual repositories, which is not a meaningful operation.
var errVirtualRepoMovement = errors.New("movements between virtual repositories are not allowed")

// errCreateItemMovementProcNoRows is returned when the
// inventory.create_item_movement_proc PL/pgSQL function (Step 7.1) yields
// zero result rows. The proc is declared RETURNS uuid and either RAISEs
// or returns the new movement id, so an empty result set indicates a
// driver-level anomaly rather than an expected branch.
var errCreateItemMovementProcNoRows = errors.New("create_item_movement_proc returned no rows")

// CreateItemMovementInput is the service-shaped DTO for
// Service.CreateItemMovement. It mirrors the fields the resolver pulls from
// the GraphQL input (ent.CreateItemMovementInput) plus the tenant ID and
// an optional uniqueness hook used to run resolver-owned validation
// (typically input-data uniqueness) before the movement is persisted.
// Keeping the hook on the DTO lets the resolver own GraphQL-shaped
// validation while the service owns orchestration timing.
type CreateItemMovementInput struct {
	// Input is the Ent-generated mutation input. The service applies it to
	// tx.ItemMovement.Create via SetInput. Using the Ent type avoids a
	// second mapping layer; it is still service-internal because the
	// resolver builds it from the GraphQL request before calling.
	Input ent.CreateItemMovementInput

	// TenantID is the tenant the movement is created under. The resolver
	// pulls it from request.ForContext(ctx).MutationTenantID().
	TenantID uuid.UUID

	// ValidateUniquenessHook, if non-nil, is invoked BEFORE
	// tx.ItemMovement.Create.Save runs. Returning a non-nil error aborts
	// the create and propagates the error verbatim, with no movement row
	// or stock-map snapshot written. Used by the resolver to run
	// validator.ValidateInputDataUniqueness with the already-validated
	// DataType (FINDINGS §3.11 / Step 5.1).
	ValidateUniquenessHook func() error
}

// ExecuteItemMovementInput is the service-shaped DTO for
// Service.ExecuteItemMovement. The execute path takes only the movement ID
// from the GraphQL request and the mutation tenant ID; everything else is
// derived from the persisted *ent.ItemMovement loaded inside the service.
// Typed UUIDs keep the service decoupled from GraphQL input shapes.
type ExecuteItemMovementInput struct {
	// ID is the item movement to execute.
	ID uuid.UUID

	// TenantID is the tenant under which the resulting transactions and
	// stock rows are written. The resolver pulls it from
	// request.ForContext(ctx).MutationTenantID().
	TenantID uuid.UUID
}

// DeleteItemMovementInput is the service-shaped DTO for
// Service.DeleteItemMovement. The delete path takes the movement ID from the
// GraphQL request, the mutation tenant ID for the reservation-clearing stock
// rows, and the deleting user ID for the soft-delete audit columns.
// Typed UUIDs keep the service decoupled from GraphQL input shapes.
type DeleteItemMovementInput struct {
	// ID is the item movement to soft-delete.
	ID uuid.UUID

	// TenantID is the tenant under which the reservation-clearing stock rows
	// are written. The resolver pulls it from
	// request.ForContext(ctx).MutationTenantID().
	TenantID uuid.UUID

	// DeletedBy is the user ID recorded as the soft-delete author. The
	// resolver pulls it from request.ForContext(ctx).User().ID.
	DeletedBy uuid.UUID
}

// errDeleteExecutedMovement is returned when attempting to delete an item
// movement that has already been executed; the underlying stock has already
// been transferred and a delete would not be reversible.
var errDeleteExecutedMovement = errors.New("cannot delete an executed movement")

// CreateRepositoryMovementInput is the service-shaped DTO for
// Service.CreateRepositoryMovement. It mirrors the fields the resolver
// pulls from the GraphQL input (ent.CreateRepositoryMovementInput) plus
// the tenant ID and an optional uniqueness hook used to run resolver-owned
// validation (typically input-data uniqueness) before the movement is
// persisted. Keeping the hook on the DTO lets the resolver own
// GraphQL-shaped validation while the service owns orchestration timing.
type CreateRepositoryMovementInput struct {
	// Input is the Ent-generated mutation input. The service applies it to
	// tx.RepositoryMovement.Create via SetInput. Using the Ent type avoids a
	// second mapping layer; it is still service-internal because the
	// resolver builds it from the GraphQL request before calling.
	Input ent.CreateRepositoryMovementInput

	// TenantID is the tenant the movement is created under. The resolver
	// pulls it from request.ForContext(ctx).MutationTenantID().
	TenantID uuid.UUID

	// ValidateUniquenessHook, if non-nil, is invoked BEFORE
	// tx.RepositoryMovement.Create.Save runs. Returning a non-nil error
	// aborts the create and propagates the error verbatim, with no
	// movement row or stock-row fan-out written. Used by the resolver to
	// run validator.ValidateInputDataUniqueness with the already-validated
	// DataType (FINDINGS §3.11 / Step 5.2).
	ValidateUniquenessHook func() error
}

// ExecuteRepositoryMovementInput is the service-shaped DTO for
// Service.ExecuteRepositoryMovement. The execute path takes only the
// movement ID from the GraphQL request and the mutation tenant ID;
// everything else is derived from the persisted *ent.RepositoryMovement
// loaded inside the service. Typed UUIDs keep the service decoupled from
// GraphQL input shapes.
type ExecuteRepositoryMovementInput struct {
	// ID is the repository movement to execute.
	ID uuid.UUID

	// TenantID is the tenant under which the resulting stock rows are
	// written. The resolver pulls it from
	// request.ForContext(ctx).MutationTenantID().
	TenantID uuid.UUID
}

// DeleteRepositoryMovementInput is the service-shaped DTO for
// Service.DeleteRepositoryMovement. The delete path takes the movement ID
// from the GraphQL request, the mutation tenant ID for the
// reservation-clearing stock rows, and the deleting user ID for the
// soft-delete audit columns. Typed UUIDs keep the service decoupled from
// GraphQL input shapes.
type DeleteRepositoryMovementInput struct {
	// ID is the repository movement to soft-delete.
	ID uuid.UUID

	// TenantID is the tenant under which the reservation-clearing stock rows
	// are written. The resolver pulls it from
	// request.ForContext(ctx).MutationTenantID().
	TenantID uuid.UUID

	// DeletedBy is the user ID recorded as the soft-delete author. The
	// resolver pulls it from request.ForContext(ctx).User().ID.
	DeletedBy uuid.UUID
}

// errStockUnderflow is returned by the underflow validator when the simulated
// stock map would leave a non-virtual repository with negative quantity.
var errStockUnderflow = errors.New("stock underflow")

// errCollectionHasMovements is returned when attempting to delete a collection
// that still has non-deleted item or repository movements attached.
var errCollectionHasMovements = errors.New("cannot delete a collection that still has movements")

// errAncestorRepoNotPreloaded is returned by the executor walks when a
// repository encountered during the parent-chain recursion is missing
// from the pre-loaded repoMap. After Phase 4.3 every executor caller
// must populate repoMap up-front via loadAncestorStocks; a missing repo
// signals a bug in the caller's seed list, not a transient lookup
// failure, so we surface a hard error rather than silently falling back
// to a per-step DB query.
var errAncestorRepoNotPreloaded = errors.New("ancestor repository not present in pre-loaded repoMap")

// CreateCollectionMovementCollectionInput is the service-shaped per-position
// element of a collection movement. It mirrors model.CollectionMovementArrayInput
// (already-validated by the resolver: data type id/slug patched, per-item data
// validated) but stays in the stock package so the service is not coupled to
// GraphQL model types.
type CreateCollectionMovementCollectionInput struct {
	// Handler is the optional per-position handler tag (free-form string the
	// resolver layer accepts as part of the GraphQL input).
	Handler string

	// DataTypeID and DataTypeSlug carry the per-position validated data type
	// the resolver patched onto the position before calling the service.
	DataTypeID   *uuid.UUID
	DataTypeSlug *string

	// Data is the per-position custom data field; validated and patched by
	// the resolver via mixin.PatchDataTypeIdSlugInput.
	Data map[string]any

	// FromID is the source repository for the movement at this position.
	// For RepositoryID-style positions it is resolved by the service against
	// the most recent non-executed movement for the same repository.
	FromID uuid.UUID

	// ToID is the destination repository.
	ToID uuid.UUID

	// ItemID, when non-nil, marks this position as an item-movement: the
	// service will create an item movement of Quantity units of ItemID.
	ItemID *uuid.UUID

	// Quantity is required for item-movement positions and rejected for
	// repository-movement positions. Carried as *float64 to preserve the
	// GraphQL nullability shape; the service casts to int64 when creating
	// the underlying ent.CreateItemMovementInput.
	Quantity *float64

	// RepositoryID, when non-nil, marks this position as a
	// repository-movement: the service will create a repository movement
	// that re-parents RepositoryID under ToID.
	RepositoryID *uuid.UUID

	// OrderID is the optional order-tracking pointer carried through the
	// per-position ent.CreateItemMovementInput / ent.CreateRepositoryMovementInput.
	OrderID *uuid.UUID
}

// CreateCollectionMovementInput is the service-shaped DTO for
// Service.CreateCollectionMovement. The resolver:
//   - generates the collection ID (so it can be returned without an extra
//     round-trip),
//   - validates the collection-level data type and patches it back into the
//     GraphQL input,
//   - validates each position's data type (and patches DataTypeID/DataTypeSlug
//     onto each CollectionMovementArrayInput),
//   - copies each position into a CreateCollectionMovementCollectionInput.
//
// The service then owns the rest: repository/parent map walks, stock map
// build, collection_movement insert, optional pre-insert uniqueness hook, and
// the per-position item/repository movement fan-out.
type CreateCollectionMovementInput struct {
	// ID is the collection movement ID. Pre-generated by the resolver via
	// uuidgql.GenerateV7UUID so it can be returned in the GraphQL output
	// without an extra Get round-trip.
	ID uuid.UUID

	// DataTypeID and DataTypeSlug carry the validated collection-level data
	// type. The resolver runs validator.ValidateDataTypeInput and patches the
	// result onto its own GraphQL input before constructing the DTO.
	DataTypeID   *uuid.UUID
	DataTypeSlug *string

	// Data is the collection-level custom data field. Already validated by
	// the resolver.
	Data map[string]any

	// Handler is the optional collection-level handler. Empty/nil means
	// "do not set"; the service mirrors the resolver's prior `*input.Handler != ""`
	// guard.
	Handler *string

	// Collection is the per-position list. Order is significant: positions
	// are inserted with sequential Position indices, and RepositoryID FromID
	// resolution looks back at earlier positions.
	Collection []CreateCollectionMovementCollectionInput

	// TenantID is the tenant under which the collection_movement,
	// item_movements, repository_movements, and resulting stock rows are
	// written. The resolver pulls it from
	// request.ForContext(ctx).MutationTenantID().
	TenantID uuid.UUID

	// PreInsertStockHook, if non-nil, is invoked after the
	// tx.Collection_Movement.Create succeeds and before the per-position
	// fan-out runs. Returning a non-nil error aborts the create and
	// propagates the error verbatim. Used by the resolver to run
	// validator.ValidateInputDataUniqueness with the collection-level
	// already-validated DataType against the collection_movements table.
	PreInsertStockHook func() error
}

// CreateCollectionMovementOutputItem is the service-shaped per-position result
// of CreateCollectionMovement. The resolver maps these into
// model.CollectionMovement values.
type CreateCollectionMovementOutputItem struct {
	// ID is the persisted item or repository movement ID.
	ID uuid.UUID

	// MovementType is "itemMovement" or "repositoryMovement", matching the
	// historical resolver payload strings.
	MovementType string
}

// CreateCollectionMovementOutput is the service-shaped result of
// Service.CreateCollectionMovement.
type CreateCollectionMovementOutput struct {
	// ID is the persisted collection_movement ID, equal to the input ID.
	ID uuid.UUID

	// Movements is the per-position result, ordered to match the input
	// Collection slice.
	Movements []CreateCollectionMovementOutputItem
}

// DeleteCollectionInput is the service-shaped DTO for
// Service.DeleteCollection. The delete path takes the collection movement ID
// from the GraphQL request and the deleting user ID for the soft-delete audit
// columns. Typed UUIDs keep the service decoupled from GraphQL input shapes.
type DeleteCollectionInput struct {
	// ID is the collection movement to soft-delete.
	ID uuid.UUID

	// DeletedBy is the user ID recorded as the soft-delete author. The
	// resolver pulls it from request.ForContext(ctx).User().ID.
	DeletedBy uuid.UUID
}

// errRebuildRepoNotFoundInRepoMap is returned during rebuild when a
// repository movement references a repository that was not present in the
// loaded repository map.
var errRebuildRepoNotFoundInRepoMap = errors.New("repository movement references repository which is not found in repoMap")

// errRebuildRepositoryNotFound is returned during rebuild event replay when a
// referenced repository cannot be located.
var errRebuildRepositoryNotFound = errors.New("repository not found during rebuild event replay")

// Service is the inventory stock domain interface. It exposes only the
// surface resolvers and main.go consume after Phase 2.9.4: the lifecycle
// methods (Close, SetDebugLog), the per-resolver entry points
// (Create/Execute/Delete item/repository/collection movements), the few
// helpers resolvers still call directly, and the rebuild entry point. The
// 13-method count exceeds the default interfacebloat threshold of 10; the
// surface is the union of resolver call sites and is intentionally
// consolidated here.
//
//nolint:interfacebloat // see comment above.
type Service interface {
	// Close releases resources held by the service. Currently a no-op,
	// kept exported because main.go does `defer stockService.Close()`.
	Close()

	// SetDebugLog installs a writer that receives structured trace lines for
	// every stock mutation (real-ops and rebuild paths). Used by tests to
	// compare execution paths. Pass nil to disable.
	SetDebugLog(w io.Writer)

	// GetCurrentRepositoriesStock loads the latest stock row per
	// (repository, item) for the given repository IDs. The resolver still
	// calls this directly from a few read paths.
	GetCurrentRepositoriesStock(ctx context.Context, tx *ent.Tx, repositoryIDs []uuid.UUID) (map[uuid.UUID]map[uuid.UUID]ent.Stock, error)

	// GetRepositoriesDetails returns the full repository map keyed by
	// repository ID. The resolver still calls this directly from a few
	// read paths.
	GetRepositoriesDetails(ctx context.Context, tx *ent.Tx) (map[uuid.UUID]ent.Repository, error)

	// CreateItemMovement orchestrates the create path for a non-collection
	// item movement: pre-flight repository and stock validation, parent-walk
	// stock-map simulation, movement insert, optional pre-insert hook, and
	// the final stock-row fan-out. Returns the persisted *ent.ItemMovement.
	CreateItemMovement(ctx context.Context, tx *ent.Tx, input CreateItemMovementInput) (*ent.ItemMovement, error)

	// ExecuteItemMovement orchestrates the execute path for an item
	// movement: load the movement, guard against double-execution, enforce
	// in-collection ordering, flip the executed flag, write the OUT/INTO
	// transaction pair, apply the FROM/TO parent-walk stock delta, and
	// fan out the resulting stock rows. Returns the updated
	// *ent.ItemMovement (with Executed=true and ExecutedAt set).
	ExecuteItemMovement(ctx context.Context, tx *ent.Tx, input ExecuteItemMovementInput) (*ent.ItemMovement, error)

	// DeleteItemMovement orchestrates the delete path for a non-executed
	// item movement: load the movement, reject already-executed movements
	// with errDeleteExecutedMovement, walk parent maps to scope the
	// reservation reversal, simulate the FROM/TO stock-map deltas in
	// subtract mode (clearing the incoming/outgoing reservations recorded
	// at create time), fan out the resulting stock rows tagged with the
	// movement ID, then soft-delete the movement. Returns the soft-deleted
	// *ent.ItemMovement (with DeletedAt and DeletedBy set).
	DeleteItemMovement(ctx context.Context, tx *ent.Tx, input DeleteItemMovementInput) (*ent.ItemMovement, error)

	// CreateRepositoryMovement orchestrates the create path for a
	// non-collection repository movement: load the moving repository and its
	// parent (the effective FROM), reject virtual-to-virtual moves, resolve
	// the effective FromID against the most recent non-executed movement
	// for the same repository, walk parent maps for RepositoryID/ToID/FromID,
	// simulate the FROM/TO stock-map deltas per item currently held by the
	// moving repository, persist the movement, run the optional pre-insert
	// hook, and fan out the resulting per-item stock rows tagged with the
	// movement ID. Returns the persisted *ent.RepositoryMovement.
	CreateRepositoryMovement(ctx context.Context, tx *ent.Tx, input CreateRepositoryMovementInput) (*ent.RepositoryMovement, error)

	// ExecuteRepositoryMovement orchestrates the execute path for a
	// repository movement: load the movement, guard against
	// double-execution, enforce in-collection ordering, flip the executed
	// flag, load the moving repository, default the FromID to the moving
	// repository when unset, fetch the per-item stock currently held by
	// the moving repository, apply the FROM/TO parent-walk stock delta in
	// a single pass per item (ownStock=false), fan out the resulting stock
	// rows tagged with the movement ID, and re-parent the moved
	// repository under the TO repository. Returns the updated
	// *ent.RepositoryMovement (with Executed=true and ExecutedAt set).
	ExecuteRepositoryMovement(ctx context.Context, tx *ent.Tx, input ExecuteRepositoryMovementInput) (*ent.RepositoryMovement, error)

	// DeleteRepositoryMovement orchestrates the delete path for a
	// non-executed repository movement: load the movement, reject
	// already-executed movements with errDeleteExecutedMovement, walk
	// parent maps for RepositoryID/ToID to scope the reservation reversal,
	// query the original per-item stock snapshot tagged with this movement
	// (the per-item quantities recorded at create time), simulate the
	// FROM/TO stock-map deltas per item in subtract mode using those
	// original quantities (ownStock=false because items are not directly in
	// the parents), fan out the resulting stock rows tagged with the
	// movement ID, then soft-delete the movement. Returns the soft-deleted
	// *ent.RepositoryMovement (with DeletedAt and DeletedBy set).
	DeleteRepositoryMovement(ctx context.Context, tx *ent.Tx, input DeleteRepositoryMovementInput) (*ent.RepositoryMovement, error)

	// CreateCollectionMovement orchestrates the create path for a collection
	// movement: collection-level repository and per-position validation
	// (virtual-virtual rejection, item-vs-repository position guard, quantity
	// guard), parent-walk stock-map simulation, collection_movement insert,
	// optional pre-insert hook (typically the resolver's input-data
	// uniqueness validation), and the per-position fan-out — item-movement
	// positions go through the package-private collection helper +
	// insertStockMap, repository-movement positions resolve their effective
	// FromID against earlier positions and the most recent non-executed
	// movement for the same repository, simulate the FROM/TO stock-map
	// deltas per item currently held by the moving repository, persist the
	// movement, and fan out the resulting per-item stock rows tagged with
	// the movement ID. Returns the service-shaped output containing the
	// pre-generated collection ID and the per-position movement IDs in input
	// order.
	CreateCollectionMovement(ctx context.Context, tx *ent.Tx, input CreateCollectionMovementInput) (CreateCollectionMovementOutput, error)

	// DeleteCollection orchestrates the delete path for a collection
	// movement: verify the collection exists, reject deletion when any
	// non-deleted item or repository movement still references it
	// (errCollectionHasMovements), and soft-delete the collection_movement
	// row. Returns the soft-deleted *ent.Collection_Movement (with DeletedAt
	// and DeletedBy set).
	DeleteCollection(ctx context.Context, tx *ent.Tx, input DeleteCollectionInput) (*ent.Collection_Movement, error)

	// RebuildStockTable clears all stock rows for the tenant and
	// reconstructs them by replaying every movement event in chronological
	// order. Called by the RebuildInventoryStock resolver.
	RebuildStockTable(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID) error
}

// New constructs a Service. The dbDialect argument is the Ent dialect string
// the surrounding *ent.Client was opened with (dialect.Postgres in
// production, dialect.SQLite in unit tests). Step 7.2 uses it to gate the
// inventory.create_item_movement_proc dispatch: the proc is a hand-written
// PL/pgSQL migration (it does not exist in the SQLite test schema), so the
// Postgres branch calls the proc and the non-Postgres branch keeps the
// legacy Go orchestration. Returning the Service interface is the whole
// point — callers (resolvers, main.go, tests) bind to the abstraction, not
// to *service.
//
//nolint:iface,ireturn // intentional: New is the single bind point.
func New(dbDialect string, outboxEmitter OutboxEmitter) (Service, error) {
	return &service{dbDialect: dbDialect, outboxEmitter: outboxEmitter}, nil
}

// OutboxEmitter is invoked by the stock service after a write that bypasses
// the Ent mutation hook (i.e. the Postgres create_item_movement_proc fast
// path) so the outbox row is still produced. Wire it to
// events.EventSystem.EmitEvent at construction. Callers MUST invoke this
// from within the same DB transaction as the bypassing write; it is the
// caller's responsibility to roll back the tx if the emit returns an error.
//
// Pass nil to disable outbox emission (e.g. in unit tests where the event
// system is not wired). When nil, the stock service skips the emit step
// silently — but production code will lose downstream workflow signals, so
// production wiring must always supply a non-nil emitter.
type OutboxEmitter func(ctx context.Context, schema, op string, entityID uuid.UUID, value entgo.Value, beforeData any) error
