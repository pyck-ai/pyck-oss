package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// ErrOCCConflict signals that an optimistic-concurrency-control check
// detected a concurrent writer. The inventory stocks ledger is the
// canonical raiser: an INSERT racing on the per-group version slot trips
// the unique index that backs the OCC scheme, and the service translates
// the resulting Postgres 23505 into this sentinel.
//
// The sentinel lives in backend/common/db (rather than the inventory
// package) so the cross-cutting transaction retry middleware in this
// same package can recognize it via errors.Is without the circular
// dependency that would arise from common/db importing an inventory
// internal. ErrIsRetryable in retry-transactions.go classifies any
// error that wraps this sentinel as retryable, alongside the existing
// 40001 (serialization failure) and 40P01 (deadlock detected) matches.
//
// Domain code that wants to participate in this retry contract should
// wrap its own sentinel onto this one (or return this one directly) at
// the point where the conflict is recognized.
var ErrOCCConflict = errors.New("optimistic concurrency conflict")

// ErrDriverLacksBeginTx is returned by pgMultiDriver.BeginTx when the
// underlying ent dialect.Driver does not satisfy the optional BeginTx
// extension interface. In production both pools are ent's *sql.Driver
// (which implements BeginTx), so this sentinel is purely defensive: it
// guards a forced type assertion against future driver swaps without
// silently panicking.
var ErrDriverLacksBeginTx = errors.New("driver does not implement BeginTx")

// PostgresError returns the SQLSTATE code and primary message of the first
// Postgres error in err's chain. Both drivers are checked: lib/pq is the one
// registered under "postgres", and pgx surfaces errors from the stored
// procedures.
func PostgresError(err error) (code, message string, ok bool) {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return string(pqErr.Code), pqErr.Message, true
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, pgErr.Message, true
	}
	return "", "", false
}

const (
	// SQLSTATE codes of the integrity-constraint violations (class 23) with
	// their own client message.
	sqlstateForeignKeyViolation = "23503"
	sqlstateUniqueViolation     = "23505"

	// Client messages for integrity-constraint violations. They keep the
	// SQLSTATE and drop the table and constraint names. Clients match on the
	// wording: workflowsdk retries a lost registration race on "23505" or
	// "duplicate key", and workers treat "duplicate key" on a create as "it
	// already exists", so the unique message keeps both.
	msgAlreadyExists    = "duplicate key: record already exists"
	msgReferenceMissing = "referenced record does not exist"
	msgStillReferenced  = "record is still referenced by other records"
	msgConstraint       = "request violates a data constraint"
)

// HideConstraintViolation returns the client message for a Postgres
// integrity-constraint violation (SQLSTATE class 23) in err's chain and logs
// the raw error, or ok=false when err carries none. The raw error names the
// table and the constraint, which maps the schema for any caller, so every
// path that turns an error into client text must go through here: the
// GraphQL error presenter and the transaction middleware, which answers a
// failed commit without the presenter.
func HideConstraintViolation(ctx context.Context, err error) (message string, ok bool) {
	code, raw, ok := PostgresError(err)
	if !ok || !strings.HasPrefix(code, "23") {
		return "", false
	}

	log.ForContext(ctx).Warn().
		Err(err).
		Str("sqlstate", code).
		Msg("constraint violation hidden from the client")
	return fmt.Sprintf("%s (SQLSTATE %s)", constraintMessage(code, raw), code), true
}

// constraintMessage picks the client message for a class 23 SQLSTATE. A
// foreign-key violation on insert or update names a missing parent row;
// on update or delete it names a row that is still referenced.
func constraintMessage(code, message string) string {
	switch code {
	case sqlstateUniqueViolation:
		return msgAlreadyExists
	case sqlstateForeignKeyViolation:
		if strings.HasPrefix(message, "update or delete") {
			return msgStillReferenced
		}
		return msgReferenceMissing
	default:
		return msgConstraint
	}
}
