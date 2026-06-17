package stock

import (
	"errors"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"

	"github.com/pyck-ai/pyck/backend/common/db"
)

// stockOCCUniqueIndex is the name of the unique index that enforces a
// monotonic version per (tenant_id, repository_id, item_id) group on the
// stocks ledger. Phase 6.1 added this index; Phase 6.2 uses it as the
// optimistic-concurrency-control conflict primitive: a concurrent INSERT
// for the same target version raises 23505 on this constraint, which the
// service surfaces as errOCCConflict.
//
// Pinned as a constant here (rather than reaching into the ent metadata)
// because the constraint-name match in wrapOCCConflict must be exact —
// matching by Code alone would catch unrelated unique violations such as
// the existing item_tenant_id_sku_deleted_at index on items.
const stockOCCUniqueIndex = "stock_tenant_id_repository_id_item_id_version"

// errOCCConflict signals that a concurrent transaction committed a stock
// row at the same per-group version that this transaction was attempting
// to insert. The append-only OCC scheme (Phase 6 design notes) treats the
// unique index as the conflict primitive, so the only way this error
// appears is via wrapOCCConflict translating a Postgres 23505 on the
// stockOCCUniqueIndex constraint.
//
// Phase 6.3 promoted the sentinel itself to backend/common/db so the
// cross-cutting transaction retry middleware can recognize it without
// importing inventory internals. This package-private alias keeps the
// existing call-site spelling (errors.Is(err, errOCCConflict)) compact;
// the underlying value is the shared db.ErrOCCConflict, so a wrapped
// inventory error satisfies errors.Is against either name.
var errOCCConflict = db.ErrOCCConflict

// wrapOCCConflict translates a Postgres unique-violation (SQLSTATE 23505)
// on the stocks per-group version index into the errOCCConflict sentinel.
// Any other error (including 23505s on unrelated constraints) is returned
// unchanged. Callers wrap the error returned by every stock INSERT site
// that could race on the version slot — the design notes explicitly
// endorse wrapping ancestor inserts too, since a 23505 there is also a
// real concurrency conflict and the same retry logic applies.
func wrapOCCConflict(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation && pgErr.ConstraintName == stockOCCUniqueIndex {
		return errOCCConflict
	}
	// lib/pq is the driver actually registered under "postgres"
	// (otelsql.Register(dialect.Postgres) in common/db/postgresql.go), so in
	// production the 23505 arrives as *pq.Error rather than *pgconn.PgError.
	// Mirror the dual-type check already in db.ErrIsRetryable; without this
	// branch the OCC sentinel never fires and the losing transaction is not
	// retried, surfacing the raw duplicate-key error to the caller.
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == pgerrcode.UniqueViolation && pqErr.Constraint == stockOCCUniqueIndex {
		return errOCCConflict
	}
	return err
}
