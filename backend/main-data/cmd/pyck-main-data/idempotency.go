package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/idempotency"

	ent "github.com/pyck-ai/pyck/backend/main-data/ent/gen"
	entidempotencykey "github.com/pyck-ai/pyck/backend/main-data/ent/gen/idempotencykey"
)

// idempotencyStore is the main-data-service implementation of
// [idempotency.Store]. See the comment on the inventory adapter for the
// full design; this file mirrors that pattern verbatim against this
// service's Ent client.
type idempotencyStore struct {
	client *ent.Client
}

func newIdempotencyStore(client *ent.Client) *idempotencyStore {
	return &idempotencyStore{client: client}
}

func (s *idempotencyStore) Lookup(
	ctx context.Context,
	key string,
	tenantID, userID uuid.UUID,
) (*idempotency.Record, error) {
	row, err := s.client.IdempotencyKey.Query().
		Where(
			entidempotencykey.Key(key),
			entidempotencykey.TenantID(tenantID),
			entidempotencykey.UserID(userID),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, idempotency.ErrNotFound
		}
		return nil, fmt.Errorf("idempotency lookup: %w", err)
	}
	return toIdempotencyRecord(row), nil
}

// LookupForResolve mirrors Lookup but forces the read onto the writer
// pool by running it inside a (rolled-back) write transaction:
// pgMultiDriver routes Ent builder queries outside a tx to the reader,
// which may lag behind the primary. ResolveRace calls this after a
// UNIQUE violation, where the row is guaranteed to exist on the primary
// but may not have reached a replica yet.
func (s *idempotencyStore) LookupForResolve(
	ctx context.Context,
	key string,
	tenantID, userID uuid.UUID,
) (*idempotency.Record, error) {
	tx, err := s.client.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("idempotency lookup-for-resolve: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	row, err := tx.IdempotencyKey.Query().
		Where(
			entidempotencykey.Key(key),
			entidempotencykey.TenantID(tenantID),
			entidempotencykey.UserID(userID),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, idempotency.ErrNotFound
		}
		return nil, fmt.Errorf("idempotency lookup-for-resolve: %w", err)
	}
	return toIdempotencyRecord(row), nil
}

func (s *idempotencyStore) InsertInFlight(
	ctx context.Context,
	rec idempotency.Record,
) error {
	tx := ent.TxFromContext(ctx)
	if tx == nil {
		return fmt.Errorf("InsertInFlight: %w", idempotency.ErrNoTxInContext)
	}
	err := tx.IdempotencyKey.Create().
		SetKey(rec.Key).
		SetTenantID(rec.TenantID).
		SetUserID(rec.UserID).
		SetOperationName(rec.OperationName).
		SetOperationChecksum(rec.OperationChecksum[:]).
		SetStatus(entidempotencykey.StatusInFlight).
		Exec(ctx)
	if err != nil {
		if idempotency.IsUniqueViolation(err) {
			return idempotency.ErrUniqueViolation
		}
		return fmt.Errorf("idempotency insert in-flight: %w", err)
	}
	return nil
}

func (s *idempotencyStore) MarkCommitted(
	ctx context.Context,
	key string,
	tenantID, userID uuid.UUID,
	response []byte,
) error {
	tx := ent.TxFromContext(ctx)
	if tx == nil {
		return fmt.Errorf("MarkCommitted: %w", idempotency.ErrNoTxInContext)
	}
	affected, err := tx.IdempotencyKey.Update().
		Where(
			entidempotencykey.Key(key),
			entidempotencykey.TenantID(tenantID),
			entidempotencykey.UserID(userID),
			entidempotencykey.StatusEQ(entidempotencykey.StatusInFlight),
		).
		SetStatus(entidempotencykey.StatusCommitted).
		SetResponse(response).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("idempotency mark committed: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: key=%q", idempotency.ErrNoInFlightRow, key)
	}
	return nil
}

func (s *idempotencyStore) Prune(ctx context.Context, olderThan time.Time) (int, error) {
	n, err := s.client.IdempotencyKey.Delete().
		Where(
			entidempotencykey.StatusEQ(entidempotencykey.StatusCommitted),
			entidempotencykey.CreatedAtLT(olderThan),
		).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("idempotency prune: %w", err)
	}
	return n, nil
}

func toIdempotencyRecord(row *ent.IdempotencyKey) *idempotency.Record {
	rec := &idempotency.Record{
		Key:           row.Key,
		TenantID:      row.TenantID,
		UserID:        row.UserID,
		OperationName: row.OperationName,
		Status:        idempotency.Status(row.Status),
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
	copy(rec.OperationChecksum[:], row.OperationChecksum)
	if row.Response != nil {
		rec.Response = *row.Response
	}
	return rec
}
