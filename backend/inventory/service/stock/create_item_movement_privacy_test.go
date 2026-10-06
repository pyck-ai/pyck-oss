//nolint:testpackage // in-package test: the Postgres harness (openPGEntClient) is package-private.
package stock

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	entgo "entgo.io/ent"
	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/request"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entitemmovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/itemmovement"
	entprivacy "github.com/pyck-ai/pyck/backend/inventory/ent/gen/privacy"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// movementPolicyFixture is a from/to repository pair under a common root
// with 10 units of one item in the from repository, seeded through the
// package's Postgres test env so the callers under test start from the same state.
type movementPolicyFixture struct {
	client   *ent.Client
	tenantID uuid.UUID
	fromID   uuid.UUID
	toID     uuid.UUID
	itemID   uuid.UUID
}

func newMovementPolicyFixture(t *testing.T) *movementPolicyFixture {
	t.Helper()

	env := newPGTestEnv(t)
	rootID := env.mkRepo("R", uuid.Nil)
	fromID := env.mkRepo("A", rootID)
	toID := env.mkRepo("B", rootID)
	itemID := env.mkItem("MOVEMENT-POLICY-" + uuid.NewString())
	env.mkStock(fromID, itemID, 10)

	return &movementPolicyFixture{
		client:   env.client,
		tenantID: env.tenantID,
		fromID:   fromID,
		toID:     toID,
		itemID:   itemID,
	}
}

// movementCallRecord captures what CreateItemMovement did besides its
// return value: whether the pre-proc uniqueness hook ran, how many outbox
// emissions the proc path made, and the rows visible in the transaction
// afterwards.
type movementCallRecord struct {
	movement   *ent.ItemMovement
	err        error
	hookCalls  int
	emitCalls  int
	movements  int
	stockRows  int
	fromQty    int64
	fromOut    int64
	emitSchema string
}

// movementCall is the entry point a fixture call goes through.
type movementCall func(*service, context.Context, *ent.Tx, CreateItemMovementInput) (*ent.ItemMovement, error)

// The two paths behind CreateItemMovement. Its entry looks the item up under
// the caller's privacy before it dispatches, so a caller who cannot read the
// tenant never reaches either path; tests of the paths' own policy call them
// directly.
var (
	viaProc movementCall = (*service).createItemMovementViaProc
	viaGo   movementCall = (*service).createItemMovementViaGo
)

// create runs one CreateItemMovement for 1 unit from -> to as the caller in
// callerCtx; see createVia.
func (f *movementPolicyFixture) create(t *testing.T, callerCtx context.Context) movementCallRecord {
	t.Helper()
	return f.createVia(t, callerCtx, (*service).CreateItemMovement)
}

// createVia runs one call for 1 unit from -> to as the caller in callerCtx,
// inside its own transaction, and records the observable effects before
// rolling back. Rows are counted under a privacy Allow decision derived from
// callerCtx so the count sees every row the call wrote.
func (f *movementPolicyFixture) createVia(t *testing.T, callerCtx context.Context, call movementCall) movementCallRecord {
	t.Helper()
	allowCtx := entprivacy.DecisionContext(callerCtx, entprivacy.Allow)

	var rec movementCallRecord
	emitter := func(_ context.Context, schema, _ string, _ uuid.UUID, _ entgo.Value, _ any) error {
		rec.emitCalls++
		rec.emitSchema = schema
		return nil
	}
	svc, err := New(dialect.Postgres, emitter)
	require.NoError(t, err)
	impl, ok := svc.(*service)
	require.True(t, ok, "New must return *service")

	tx, err := f.client.Tx(allowCtx)
	require.NoError(t, err)
	t.Cleanup(func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			t.Logf("rollback: %v", rerr)
		}
	})

	rec.movement, rec.err = call(impl, callerCtx, tx, CreateItemMovementInput{
		Input: ent.CreateItemMovementInput{
			Quantity: 1,
			Handler:  "policy-test",
			FromID:   f.fromID,
			ToID:     f.toID,
			ItemID:   f.itemID,
		},
		TenantID: f.tenantID,
		ValidateUniquenessHook: func() error {
			rec.hookCalls++
			return nil
		},
	})

	rec.movements, err = tx.ItemMovement.Query().Where(entitemmovement.TenantID(f.tenantID)).Count(allowCtx)
	require.NoError(t, err)
	rec.stockRows, err = tx.Stock.Query().Where(entstock.TenantID(f.tenantID)).Count(allowCtx)
	require.NoError(t, err)
	current, err := tx.Stock.Query().
		Where(entstock.RepositoryID(f.fromID), entstock.ItemID(f.itemID)).
		Order(ent.Desc(entstock.FieldVersion)).
		First(allowCtx)
	require.NoError(t, err)
	rec.fromQty = current.Quantity
	rec.fromOut = current.OutgoingStock

	return rec
}

// callerCtx builds the request context a resolver would hand the service
// for user acting in the fixture's tenant.
func (f *movementPolicyFixture) callerCtx(user *authn.User) context.Context {
	return request.Context(context.Background(), user, f.tenantID)
}

// On Postgres CreateItemMovement writes through inventory.create_item_movement_proc,
// raw SQL that ent's privacy hook never sees. The proc path must refuse
// every caller the ItemMovement mutation policy refuses, before the
// uniqueness hook or the proc run, so nothing is written or emitted.
func TestCreateItemMovementViaProc_RefusesCallersWithoutWriterRole(t *testing.T) {
	t.Parallel()

	otherTenant := uuid.New()

	cases := []struct {
		name    string
		user    func(tenantID uuid.UUID) *authn.User
		errText string
	}{
		{
			name: "reader of the tenant",
			user: func(tenantID uuid.UUID) *authn.User {
				return &authn.User{
					ID:       uuid.New(),
					TenantID: tenantID,
					Roles:    map[uuid.UUID]authn.Role{tenantID: authn.ROLE_READER},
				}
			},
			errText: "does not have writer role",
		},
		{
			name: "writer of another tenant only",
			user: func(tenantID uuid.UUID) *authn.User {
				return &authn.User{
					ID:       uuid.New(),
					TenantID: tenantID,
					Roles:    map[uuid.UUID]authn.Role{otherTenant: authn.ROLE_WRITER},
				}
			},
			errText: "does not have writer role",
		},
		{
			name: "authenticated user without any role",
			user: func(tenantID uuid.UUID) *authn.User {
				return &authn.User{ID: uuid.New(), TenantID: tenantID}
			},
			errText: "does not have writer role",
		},
		{
			name: "unauthenticated caller",
			user: func(uuid.UUID) *authn.User {
				return &authn.User{}
			},
			errText: "no user",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMovementPolicyFixture(t)

			rec := f.createVia(t, f.callerCtx(tc.user(f.tenantID)), viaProc)

			require.ErrorIs(t, rec.err, mixin.ErrUnauthorized)
			require.ErrorIs(t, rec.err, entprivacy.Deny)
			require.ErrorContains(t, rec.err, tc.errText)
			assert.Nil(t, rec.movement)
			assert.Zero(t, rec.hookCalls, "the policy must refuse before the uniqueness hook runs")
			assert.Zero(t, rec.emitCalls, "a refused caller must not emit a movement event")
			assert.Zero(t, rec.movements, "a refused caller must not leave a movement behind")
			assert.Equal(t, 1, rec.stockRows, "a refused caller must not write stock snapshots")
			assert.Equal(t, int64(10), rec.fromQty, "a refused caller must not change the from stock")
			assert.Zero(t, rec.fromOut, "a refused caller must not reserve outgoing stock")
		})
	}
}

// The guard must not lock out the callers the Go path lets through: a
// writer, the system user, and internal code that runs under an explicit
// privacy Allow decision without any role.
func TestCreateItemMovementViaProc_AllowsCallersThePolicyAllows(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ctx  func(f *movementPolicyFixture) context.Context
	}{
		{
			name: "writer of the tenant",
			ctx: func(f *movementPolicyFixture) context.Context {
				return f.callerCtx(&authn.User{
					ID:       uuid.New(),
					TenantID: f.tenantID,
					Roles:    map[uuid.UUID]authn.Role{f.tenantID: authn.ROLE_WRITER},
				})
			},
		},
		{
			name: "system user",
			ctx: func(f *movementPolicyFixture) context.Context {
				return f.callerCtx(&authn.User{ID: uuid.Max, TenantID: uuid.Max})
			},
		},
		{
			name: "privacy allow decision without any role",
			ctx: func(f *movementPolicyFixture) context.Context {
				return entprivacy.DecisionContext(
					f.callerCtx(&authn.User{ID: uuid.New(), TenantID: f.tenantID}),
					entprivacy.Allow,
				)
			},
		},
		{
			name: "privacy allow decision for a reader",
			ctx: func(f *movementPolicyFixture) context.Context {
				return entprivacy.DecisionContext(
					f.callerCtx(&authn.User{
						ID:       uuid.New(),
						TenantID: f.tenantID,
						Roles:    map[uuid.UUID]authn.Role{f.tenantID: authn.ROLE_READER},
					}),
					entprivacy.Allow,
				)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMovementPolicyFixture(t)

			rec := f.create(t, tc.ctx(f))

			require.NoError(t, rec.err)
			require.NotNil(t, rec.movement)
			assert.Equal(t, f.tenantID, rec.movement.TenantID)
			assert.Equal(t, f.itemID, rec.movement.ItemID)
			assert.Equal(t, int64(1), rec.movement.Quantity)
			assert.Equal(t, 1, rec.hookCalls, "the uniqueness hook must still run once")
			// The manual emitter only fires on the proc path, so one call
			// proves the proc (not the Go path) wrote this movement.
			assert.Equal(t, 1, rec.emitCalls, "the proc path must emit exactly one movement event")
			assert.Equal(t, "ItemMovement", rec.emitSchema)
			assert.Equal(t, 1, rec.movements)
			// An unexecuted movement reserves the quantity as outgoing stock
			// on the from repository; the on-hand quantity is unchanged.
			assert.Equal(t, int64(10), rec.fromQty)
			assert.Equal(t, int64(1), rec.fromOut, "the from stock must reserve the moved quantity")
		})
	}
}

// Both CreateItemMovement paths must refuse a reader with the same error,
// so a client cannot tell (or exploit) which path ran.
func TestCreateItemMovement_ProcAndGoPathRefuseReaderAlike(t *testing.T) {
	t.Parallel()

	f := newMovementPolicyFixture(t)
	reader := &authn.User{
		ID:       uuid.MustParse("0198a6b2-0000-7000-8000-00000000d001"),
		TenantID: f.tenantID,
		Roles:    map[uuid.UUID]authn.Role{f.tenantID: authn.ROLE_READER},
	}
	readerCtx := f.callerCtx(reader)

	procRec := f.create(t, readerCtx)
	goRec := f.create(t, WithDeferredUnderflow(readerCtx))

	require.ErrorIs(t, procRec.err, entprivacy.Deny)
	require.ErrorIs(t, goRec.err, entprivacy.Deny)
	assert.Equal(t, goRec.err.Error(), procRec.err.Error(),
		"the proc path must refuse with the Go path's error text")
	assert.Zero(t, procRec.movements)
	assert.Zero(t, goRec.movements)
}
