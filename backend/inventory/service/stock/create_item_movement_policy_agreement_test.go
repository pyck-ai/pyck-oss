//nolint:testpackage // in-package test: the Postgres harness (openPGEntClient) is package-private.
package stock

import (
	"context"
	"testing"

	entgo "entgo.io/ent"
	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entitemmovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/itemmovement"
	entprivacy "github.com/pyck-ai/pyck/backend/inventory/ent/gen/privacy"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// The proc path's policy guard claims to refuse exactly the callers the Go
// path refuses. This table drives both paths with the same caller contexts,
// including the ones a role-only check gets wrong: a privacy Deny decision
// over a writer, an admin, a caller whose roles span several tenants, and a
// writer whose own home tenant differs from the request tenant. A context
// one path accepts and the other refuses is a bypass (or a lock-out) of the
// ItemMovement policy.
func TestCreateItemMovement_ProcAndGoPathAgreeOnEveryCaller(t *testing.T) {
	t.Parallel()

	otherTenant := uuid.New()

	cases := []struct {
		name  string
		ctx   func(f *movementPolicyFixture) context.Context
		allow bool
	}{
		{
			name: "reader",
			ctx: func(f *movementPolicyFixture) context.Context {
				return f.callerCtx(roleUser(f.tenantID, f.tenantID, authn.ROLE_READER))
			},
		},
		{
			name: "writer",
			ctx: func(f *movementPolicyFixture) context.Context {
				return f.callerCtx(roleUser(f.tenantID, f.tenantID, authn.ROLE_WRITER))
			},
			allow: true,
		},
		{
			name: "admin",
			ctx: func(f *movementPolicyFixture) context.Context {
				return f.callerCtx(roleUser(f.tenantID, f.tenantID, authn.ROLE_ADMIN))
			},
			allow: true,
		},
		{
			name: "writer under an explicit privacy deny decision",
			ctx: func(f *movementPolicyFixture) context.Context {
				return entprivacy.DecisionContext(
					f.callerCtx(roleUser(f.tenantID, f.tenantID, authn.ROLE_WRITER)),
					entprivacy.Deny,
				)
			},
		},
		{
			name: "system user under an explicit privacy deny decision",
			ctx: func(f *movementPolicyFixture) context.Context {
				return entprivacy.DecisionContext(
					f.callerCtx(&authn.User{ID: uuid.Max, TenantID: uuid.Max}),
					entprivacy.Deny,
				)
			},
		},
		{
			name: "reader here and writer in another tenant",
			ctx: func(f *movementPolicyFixture) context.Context {
				return f.callerCtx(&authn.User{
					ID:       uuid.New(),
					TenantID: otherTenant,
					Roles: map[uuid.UUID]authn.Role{
						f.tenantID:  authn.ROLE_READER,
						otherTenant: authn.ROLE_WRITER,
					},
				})
			},
		},
		{
			name: "writer here whose home tenant is another one",
			ctx: func(f *movementPolicyFixture) context.Context {
				return f.callerCtx(roleUser(otherTenant, f.tenantID, authn.ROLE_WRITER))
			},
			allow: true,
		},
		{
			name: "role none recorded for the tenant",
			ctx: func(f *movementPolicyFixture) context.Context {
				return f.callerCtx(roleUser(f.tenantID, f.tenantID, authn.ROLE_NONE))
			},
		},
		{
			name: "user id set but home tenant nil",
			ctx: func(f *movementPolicyFixture) context.Context {
				return f.callerCtx(&authn.User{
					ID:    uuid.New(),
					Roles: map[uuid.UUID]authn.Role{f.tenantID: authn.ROLE_WRITER},
				})
			},
		},
		{
			name: "system id with a real home tenant is not the system user",
			ctx: func(f *movementPolicyFixture) context.Context {
				return f.callerCtx(&authn.User{ID: uuid.Max, TenantID: f.tenantID})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// One fixture per path: an accepted call holds row locks until
			// its transaction rolls back at cleanup, so a second call on the
			// same rows would block.
			f := newMovementPolicyFixture(t)
			procRec := f.createVia(t, tc.ctx(f), viaProc)
			g := newMovementPolicyFixture(t)
			goRec := g.createVia(t, tc.ctx(g), viaGo)

			if tc.allow {
				require.NoError(t, goRec.err, "Go path")
				require.NoError(t, procRec.err, "proc path")
				assert.Equal(t, 1, procRec.movements)
				assert.Equal(t, 1, procRec.emitCalls)
				return
			}
			require.ErrorIs(t, goRec.err, entprivacy.Deny, "Go path")
			require.ErrorIs(t, procRec.err, entprivacy.Deny, "proc path")
			assert.Zero(t, procRec.hookCalls, "the proc path must refuse before the uniqueness hook")
			assert.Zero(t, procRec.emitCalls)
			assert.Zero(t, procRec.movements)
			assert.Equal(t, 1, procRec.stockRows)
			assert.Zero(t, procRec.fromOut)
		})
	}
}

// roleUser is a user homed in home with a single role in roleTenant.
func roleUser(home, roleTenant uuid.UUID, role authn.Role) *authn.User {
	return &authn.User{
		ID:       uuid.New(),
		TenantID: home,
		Roles:    map[uuid.UUID]authn.Role{roleTenant: role},
	}
}

// The policy decides on the request tenant (MutationTenantID); the proc
// writes into dto.TenantID. A writer of tenant A handing the service a dto
// for tenant B passes the policy for A. No production caller builds such a
// dto (the resolver copies MutationTenantID into it, the collection caller
// takes the Go path), but the service must still refuse it, emit nothing,
// and leave nothing in B once the caller rolls back on the error, as every
// caller of CreateItemMovement does with the transaction it owns.
//
// The refusal on the proc path comes late: the proc has already written the
// movement and reserved stock in B inside the transaction, and only the
// tenant-scoped reload of the movement fails. This test pins the committed
// outcome so neither that reload nor the rollback can be dropped silently.
func TestCreateItemMovementViaProc_DtoTenantOtherThanRequestTenantCommitsNothing(t *testing.T) {
	t.Parallel()

	f := newMovementPolicyFixture(t) // tenant B: the dto's tenant, holds the rows
	tenantA := uuid.New()
	writerOfA := request.Context(context.Background(), roleUser(tenantA, tenantA, authn.ROLE_WRITER), tenantA)
	allowB := entprivacy.DecisionContext(f.callerCtx(&authn.User{ID: uuid.New(), TenantID: f.tenantID}), entprivacy.Allow)

	for _, path := range []struct {
		name  string
		route func(context.Context) context.Context
	}{
		{name: "proc", route: func(ctx context.Context) context.Context { return ctx }},
		{name: "go", route: WithDeferredUnderflow},
	} {
		emits := 0
		emitter := func(context.Context, string, string, uuid.UUID, entgo.Value, any) error {
			emits++
			return nil
		}
		svc, err := New(dialect.Postgres, emitter)
		require.NoError(t, err)
		tx, err := f.client.Tx(allowB)
		require.NoError(t, err)

		_, callErr := svc.CreateItemMovement(path.route(writerOfA), tx, CreateItemMovementInput{
			Input: ent.CreateItemMovementInput{
				Quantity: 1, Handler: "policy-test", FromID: f.fromID, ToID: f.toID, ItemID: f.itemID,
			},
			TenantID: f.tenantID,
		})
		require.NoError(t, tx.Rollback(), path.name)

		// Both paths refuse through a read scoped to the request tenant:
		// the Go path cannot load B's repository, the proc path cannot
		// reload the movement it just wrote into B.
		require.Error(t, callErr, path.name)
		assert.True(t, ent.IsNotFound(callErr), "%s: want a tenant-scoped not-found, got %v", path.name, callErr)
		assert.Zero(t, emits, "%s: a refused call must not emit a movement event", path.name)
	}

	movements, err := f.client.ItemMovement.Query().Where(entitemmovement.TenantID(f.tenantID)).Count(allowB)
	require.NoError(t, err)
	assert.Zero(t, movements, "no movement may be committed in tenant B")
	current, err := f.client.Stock.Query().
		Where(entstock.RepositoryID(f.fromID), entstock.ItemID(f.itemID)).
		Order(ent.Desc(entstock.FieldVersion)).
		First(allowB)
	require.NoError(t, err)
	assert.Zero(t, current.OutgoingStock, "no stock of tenant B may stay reserved")
}
