//nolint:testpackage // in-package test required: mapCreateItemMovementProcError and the *service struct are package-private.
package stock

import (
	"context"
	"errors"
	"fmt"
	"testing"

	entgo "entgo.io/ent"
	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/enttest"
	entitemmovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/itemmovement"
	entprivacy "github.com/pyck-ai/pyck/backend/inventory/ent/gen/privacy"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
)

// TestMapCreateItemMovementProcError pins the proc error string contract:
// the PL/pgSQL function in 20260430070249_create_item_movement_proc.up.sql
// raises EXCEPTION with specific wording for the two business errors
// (virtual-virtual move, insufficient stock); the Go side must translate
// those back to the same sentinels createItemMovementViaGo returns so
// callers see one error shape regardless of which path executed. A 23505
// on the stocks OCC index (the proc's retry budget can ultimately reraise
// it) must surface as errOCCConflict for gqltx's retry middleware to
// engage. Everything else passes through unchanged.
func TestMapCreateItemMovementProcError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		in     error
		expect error // nil means "pass through unchanged"
	}{
		{
			name:   "nil passthrough",
			in:     nil,
			expect: nil,
		},
		{
			name:   "virtual-virtual movement string maps to sentinel",
			in:     errors.New("ERROR: movements between virtual repositories are not allowed"),
			expect: errVirtualRepoMovement,
		},
		{
			name:   "stock-insufficient string maps to sentinel",
			in:     errors.New("ERROR: STOCK_INSUFFICIENT: from=... item=... need=5 have=2"),
			expect: errInsufficientStock,
		},
		{
			name: "23505 on the stocks OCC index maps to errOCCConflict",
			in: fmt.Errorf("proc reraise: %w", &pgconn.PgError{
				Code:           "23505",
				ConstraintName: stockOCCUniqueIndex,
			}),
			expect: errOCCConflict,
		},
		{
			name:   "unrelated error passes through verbatim",
			in:     errors.New("connection reset by peer"),
			expect: nil, // verified below by identity comparison
		},
		{
			name: "23505 on a different constraint passes through",
			in: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "item_tenant_id_sku_deleted_at",
			},
			expect: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := mapCreateItemMovementProcError(tc.in)
			switch {
			case tc.in == nil:
				require.NoError(t, out)
			case tc.expect != nil:
				require.ErrorIs(t, out, tc.expect)
			default:
				// Pass-through: out must equal in (or wrap to the same).
				require.Equal(t, tc.in, out, "unmapped error should not be transformed")
			}
		})
	}
}

// TestNew_StoresOutboxEmitter pins the constructor wiring: the emitter
// passed to stock.New is reachable on the package-private *service so
// createItemMovementViaProc can invoke it after the proc returns. The
// in-package access lets the test observe the field directly without
// exporting it just to be testable. Regressions here would silently drop
// outbox emission on the Postgres fast path (no NATS event, resolver
// waits OutboxReplyTimeout for a workflow reply that never arrives).
func TestNew_StoresOutboxEmitter(t *testing.T) {
	t.Parallel()

	called := false
	emitter := func(_ context.Context, _, _ string, _ uuid.UUID, _ entgo.Value, _ any) error {
		called = true
		return nil
	}

	svc, err := New(dialect.Postgres, emitter)
	require.NoError(t, err)

	impl, ok := svc.(*service)
	require.True(t, ok, "New must return the package-private *service")
	require.NotNil(t, impl.outboxEmitter, "outboxEmitter field must be populated")
	require.Equal(t, dialect.Postgres, impl.dbDialect, "dbDialect field must be populated")

	// Sanity: the stored value is the function we passed, not some wrapper
	// that drops the invocation.
	require.NoError(t, impl.outboxEmitter(context.Background(), "x", "create", uuid.Nil, nil, nil))
	require.True(t, called, "stored emitter must be the one passed to New")
}

// TestNew_NilEmitterTolerated pins the SQLite-test contract: New accepts
// a nil emitter, and createItemMovementViaProc's emit step is guarded by
// `if s.outboxEmitter != nil` so the proc fast path stays runnable in
// environments where the event system is not wired (most unit tests).
// Production wiring in main.go always supplies a non-nil emitter; this
// test just locks the guard so a refactor cannot drop it and crash every
// SQLite test that constructs the service with `nil`.
func TestNew_NilEmitterTolerated(t *testing.T) {
	t.Parallel()

	svc, err := New(dialect.SQLite, nil)
	require.NoError(t, err)

	impl, ok := svc.(*service)
	require.True(t, ok)
	require.Nil(t, impl.outboxEmitter)
}

// TestCreateItemMovement_GoPathDoesNotInvokeEmitter pins the boundary
// between the two CreateItemMovement paths: the manual outbox emitter is
// reserved for createItemMovementViaProc (the Postgres fast path that
// bypasses the Ent mutation hook). The Go orchestration path uses the
// regular Ent .Save flow, which fires the registered mutation hook so
// the outbox row is written through that channel. Calling the manual
// emitter from the Go path would produce duplicate outbox rows. This
// test wires a recording emitter, runs CreateItemMovement on the SQLite
// Go path, and asserts the emitter was never invoked.
func TestCreateItemMovement_GoPathDoesNotInvokeEmitter(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, testresolver.DatabaseURI(t))
	t.Cleanup(func() { _ = client.Close() })

	tenantID := uuid.New()
	user := &authn.User{ID: uuid.New(), TenantID: tenantID}
	ctx := request.Context(context.Background(), user, tenantID)
	ctx = entprivacy.DecisionContext(ctx, entprivacy.Allow)

	mkRepo := func(name string, parent uuid.UUID) uuid.UUID {
		t.Helper()
		b := client.Repository.Create().
			SetTenantID(tenantID).
			SetName(name).
			SetType(entrepository.TypeStatic).
			SetVirtualRepo(false)
		if parent != uuid.Nil {
			b.SetParentID(parent)
		}
		repo, err := b.Save(ctx)
		require.NoError(t, err)
		return repo.ID
	}

	rootID := mkRepo("R", uuid.Nil)
	fromID := mkRepo("A", rootID)
	toID := mkRepo("B", rootID)

	item, err := client.Item.Create().
		SetTenantID(tenantID).
		SetSku("CREATE-ITEM-MV-GO-PATH-NO-EMIT").
		Save(ctx)
	require.NoError(t, err)
	itemID := item.ID

	const seedQty int64 = 5
	_, err = client.Stock.Create().
		SetTenantID(tenantID).
		SetRepositoryID(fromID).
		SetItemID(itemID).
		SetQuantity(seedQty).
		SetOwnQuantity(seedQty).
		SetMovementID(uuid.New()).
		Save(ctx)
	require.NoError(t, err)

	emitCalls := 0
	emitter := func(_ context.Context, _, _ string, _ uuid.UUID, _ entgo.Value, _ any) error {
		emitCalls++
		return nil
	}

	// dialect.SQLite (not Postgres) → CreateItemMovement routes through
	// createItemMovementViaGo. The manual emitter must stay quiet.
	svc, err := New(dialect.SQLite, emitter)
	require.NoError(t, err)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	movement, err := svc.CreateItemMovement(ctx, tx, CreateItemMovementInput{
		Input: ent.CreateItemMovementInput{
			Quantity: 1,
			Handler:  "test",
			FromID:   fromID,
			ToID:     toID,
			ItemID:   itemID,
		},
		TenantID: tenantID,
	})
	require.NoError(t, err)
	require.NotNil(t, movement)

	// Confirm the movement actually landed via the Go path (Ent flow),
	// so the assertion below is meaningful — otherwise a regression that
	// silently skipped the create would also pass the emitCalls check.
	gotMovement, err := tx.ItemMovement.Query().Where(entitemmovement.TenantID(tenantID)).First(ctx)
	require.NoError(t, err)
	require.Equal(t, movement.ID, gotMovement.ID)

	require.Equal(t, 0, emitCalls,
		"the Go createItemMovementViaGo path must never invoke the manual outbox emitter — that is reserved for the proc fast path")
}
