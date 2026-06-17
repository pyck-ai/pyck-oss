package db

import "errors"

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
