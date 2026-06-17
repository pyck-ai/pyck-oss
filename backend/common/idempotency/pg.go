package idempotency

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

// pgUniqueViolation is the Postgres SQLSTATE for a unique_violation
// error. Used to distinguish a UNIQUE constraint failure (which the
// gqltx middleware re-routes through ResolveRace) from other constraint
// failures (CHECK, FOREIGN KEY, NOT NULL) which must surface as 500s.
const pgUniqueViolation = "23505"

// IsUniqueViolation reports whether err is a Postgres unique_violation
// (SQLSTATE 23505). Both lib/pq and pgx error types are recognized so
// per-service adapters do not need to care which driver Ent is using
// today. Other constraint failures (CHECK, FOREIGN KEY) return false
// and should be surfaced as a generic store error.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return string(pqErr.Code) == pgUniqueViolation
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgUniqueViolation
	}
	return false
}
