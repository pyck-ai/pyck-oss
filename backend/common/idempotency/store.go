package idempotency

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Store abstracts persistence for idempotency records.
//
// Each backend service supplies its own implementation backed by that
// service's Ent client. Lookup and Prune run outside any transaction;
// InsertInFlight and MarkCommitted must run inside the GraphQL
// mutation's transaction and therefore expect the per-service Ent tx to
// be carried on the context (via ent.TxFromContext).
type Store interface {
	// Lookup returns the record for the given tuple, or [ErrNotFound] if
	// none exists. It must not require an open transaction. May be
	// served by a read replica: PreCheck tolerates a stale miss because
	// the writer-side UNIQUE constraint is the actual duplicate guard.
	Lookup(ctx context.Context, key string, tenantID, userID uuid.UUID) (*Record, error)

	// LookupForResolve is Lookup with read-your-writes consistency: it
	// MUST read from the writer (primary) so a row committed
	// milliseconds ago by a concurrent winner is visible. [ResolveRace]
	// uses it after a UNIQUE violation — the plain Lookup may be served
	// by a lagging replica, and resolving the race against stale data
	// would turn the contractual "replay the winner's response" into a
	// spurious RACE_GHOST 500 for exactly the network-retry case this
	// feature exists to serve.
	LookupForResolve(ctx context.Context, key string, tenantID, userID uuid.UUID) (*Record, error)

	// InsertInFlight inserts a row in status [StatusInFlight] inside the
	// transaction carried on ctx. It returns [ErrUniqueViolation] if a row
	// for the same (key, tenant, user) already exists; the caller must
	// roll back the surrounding transaction in that case.
	InsertInFlight(ctx context.Context, rec Record) error

	// MarkCommitted updates the in-flight row identified by
	// (key, tenant, user), setting status to [StatusCommitted] and storing
	// the serialized response. Runs inside the transaction carried on ctx.
	MarkCommitted(ctx context.Context, key string, tenantID, userID uuid.UUID, response []byte) error

	// Prune removes committed records older than the given cutoff and
	// returns the number of rows deleted. Runs outside any transaction;
	// safe to call from a background goroutine.
	Prune(ctx context.Context, olderThan time.Time) (int, error)
}
