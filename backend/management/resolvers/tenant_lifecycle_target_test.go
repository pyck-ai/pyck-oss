package resolvers_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/entityeventsoutbox"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	"github.com/pyck-ai/pyck/backend/management/ent/types"
)

// The three tenant-lifecycle mutations now name their target explicitly
// instead of inferring it from the caller's tenant header. That moves two
// things this file pins: which tenant a caller may act on (the role check
// is against the named target, not against the header), and who the audit
// trail records for the write (the caller, not the system user the
// resolver used to swap to).

// testRegistrationData is the admin account a pending or failed
// registration row carries: the data the delete must clear, and the reason
// a running registration must not be retired underneath its run.
func testRegistrationData() *types.TenantRegistration {
	return &types.TenantRegistration{
		AdminUsername:  "admin",
		AdminEmail:     "admin@example.com",
		AdminFirstName: "Ada",
		AdminLastName:  "Admin",
		PasswordHash:   "$2a$12$hash",
	}
}

// outboxRows reads the outbox in write order so a test can assert who the
// event records as the acting user, not merely that an event happened.
func outboxRows(t *testing.T, te *testEnv, ctx context.Context) []*ent.EntityEventsOutbox {
	t.Helper()
	rows, err := te.Ent.EntityEventsOutbox.Query().
		Order(ent.Asc(entityeventsoutbox.FieldCreatedAt)).
		All(ctx)
	require.NoError(t, err)
	return rows
}

// =============================================================================
// deleteTenant: the target is the input, not the header
// =============================================================================

// A tenant admin holds ROLE_ADMIN on its own tenant only. Now that the
// target is an argument, the role check has to be made against that
// argument; otherwise any tenant admin could retire any tenant by naming
// its id, which is precisely the hole the explicit target opens if the gate
// is left reading the caller's own tenant.
func TestDeleteTenant_RejectsACrossTenantTarget(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Name("CallerTenant").Create()
	te.newTenant(te.ctxForTenant(systemUser, resolver.TenantB), resolver.TenantB).Name("ForeignTenant").Create()

	// userA is ROLE_ADMIN on TenantA and nothing on TenantB.
	execErr(te, te.ctx(userA), deleteTenantMut, tenantArgs(resolver.TenantB, nil), "admin role required")

	got := mustGetTenant(t, te, resolver.TenantB)
	assert.True(t, got.DeletedAt.IsZero(), "the foreign tenant must survive a refused deleteTenant")
}

// The refusal must not double as an existence oracle either: naming a
// tenant id that does not exist has to read the same way as naming one the
// caller may not touch, because the role check is what answers first.
func TestDeleteTenant_UnknownTargetReadsAsARefusalForANonAdmin(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Name("CallerTenant").Create()

	unknown := uuidgql.GenerateV7UUID()
	execErr(te, te.ctx(userA), deleteTenantMut, tenantArgs(unknown, nil), "admin role required")
}

// The tenant's own admin may delete its pending tenant; the registration
// run is left to notice the deleted row itself.
func TestDeleteTenant_TheTenantsOwnAdminRetiresItsPendingRow(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	sysCtx := te.ctxForTenant(systemUser, resolver.TenantA)
	te.newTenant(sysCtx, resolver.TenantA).
		Name("SelfRegistering").
		Status(enttenant.StatusPending).
		Registration(testRegistrationData()).
		Create()

	data := execOK[deleteTenantData](te, te.ctx(userA), deleteTenantMut, tenantArgs(resolver.TenantA, nil))
	assert.True(t, data.DeleteTenant.Success)

	got := mustGetTenant(t, te, resolver.TenantA)
	assert.False(t, got.DeletedAt.IsZero(), "a pending row is soft-deleted")
	assert.Nil(t, got.Registration, "the registration data does not outlive the delete")
}

// A failed registration is retired, not protected: nothing is running on
// the row, the operator's re-run option is what the delete ends, and the
// admin password hash must not outlive it.
func TestDeleteTenant_AcceptsASettledRow(t *testing.T) {
	t.Parallel()

	for _, status := range []enttenant.Status{enttenant.StatusDone, enttenant.StatusError} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)

			tenantID := uuidgql.GenerateV7UUID()
			ctx := te.ctxForTenant(systemUser, tenantID)
			te.newTenant(ctx, tenantID).
				Name("Settled-" + string(status)).
				Status(status).
				Registration(testRegistrationData()).
				Create()
			te.clearEvents(ctx)

			data := execOK[deleteTenantData](te, ctx, deleteTenantMut, tenantArgs(tenantID, nil))
			require.True(t, data.DeleteTenant.Success)

			got := mustGetTenant(t, te, tenantID)
			assert.False(t, got.DeletedAt.IsZero(), "a settled row is retired")
			assert.Nil(t, got.Registration, "the admin password hash must not outlive the row's use")
			assert.Equal(t, status, got.Status, "the delete does not rewrite the registration outcome")
			te.assertEvents(ctx, Delete("tenant", tenantID))
		})
	}
}

// HistoryMixin stamps deleted_by from the context user and overwrites any
// explicit value, so the audit trail is only real if the write runs under
// the caller's identity. A resolver that swaps to the system user before
// the write records "system" for every disable, whoever asked for it.
func TestDeleteTenant_RecordsTheCallerAsDeletedBy(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Name("Audited").Create()
	ctx := te.ctx(userA)
	te.clearEvents(ctx)

	data := execOK[deleteTenantData](te, ctx, deleteTenantMut, tenantArgs(resolver.TenantA, nil))
	require.True(t, data.DeleteTenant.Success)

	got := mustGetTenant(t, te, resolver.TenantA)
	require.False(t, got.DeletedAt.IsZero())
	assert.Equal(t, userA.ID, got.DeletedBy,
		"the audit trail must name the admin who disabled the tenant, not the system user")

	rows := outboxRows(t, te, ctx)
	require.Len(t, rows, 1, "the delete emits exactly one event")
	require.NotNil(t, rows[0].UserID, "the outbox row must carry the acting user")
	assert.Equal(t, userA.ID, *rows[0].UserID)
	assert.Equal(t, resolver.TenantA, rows[0].TenantID,
		"the event topic tenant is the target: Tenant has no tenant_id column of its own")
	assert.Contains(t, rows[0].Topic, resolver.TenantA.String()+".delete")
}

// The same write performed by an operator records the operator. This is the
// other half of the audit assertion: a resolver that hardcoded the caller
// where it used to hardcode the system user would pass the test above and
// fail here.
func TestDeleteTenant_RecordsTheSystemCallerWhenAnOperatorDisables(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	tenantID := uuidgql.GenerateV7UUID()
	ctx := te.ctxForTenant(systemUser, tenantID)
	te.newTenant(ctx, tenantID).Name("OperatorDisabled").Create()
	te.clearEvents(ctx)

	require.True(t, execOK[deleteTenantData](te, ctx, deleteTenantMut, tenantArgs(tenantID, nil)).DeleteTenant.Success)

	got := mustGetTenant(t, te, tenantID)
	assert.Equal(t, systemUser.ID, got.DeletedBy, "an operator's disable is stamped with the system user")

	rows := outboxRows(t, te, ctx)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].UserID)
	assert.Equal(t, systemUser.ID, *rows[0].UserID)
	assert.Equal(t, tenantID, rows[0].TenantID)
}

// =============================================================================
// restoreTenant: only a finished registration comes back
// =============================================================================

// A pending row has a run that still owns it and an error row lost its
// registration data at the delete, so restoring either produces a tenant
// nothing ever provisioned: an org that does not exist, no admin, no
// namespace, live as far as every other service can tell.
func TestRestoreTenant_RefusesAnUnfinishedRegistration(t *testing.T) {
	t.Parallel()

	for _, status := range []enttenant.Status{enttenant.StatusPending, enttenant.StatusError} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)

			tenantID := uuidgql.GenerateV7UUID()
			ctx := te.ctxForTenant(systemUser, tenantID)
			te.newTenant(ctx, tenantID).
				Name("Unfinished-" + string(status)).
				Status(status).
				Deleted().
				Create()
			te.clearEvents(ctx)

			execErr(te, ctx, restoreTenantMut, tenantArgs(tenantID, nil), "tenant registration did not finish")

			got := mustGetTenant(t, te, tenantID)
			assert.False(t, got.DeletedAt.IsZero(), "the row must stay retired")
			te.assertNoEvents(ctx)
		})
	}
}

// The complement of the refusal above: a finished registration is exactly
// what restore is for, so the status gate must not swallow the one case it
// has to admit.
func TestRestoreTenant_AcceptsAFinishedRegistration(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	tenantID := uuidgql.GenerateV7UUID()
	ctx := te.ctxForTenant(systemUser, tenantID)
	te.newTenant(ctx, tenantID).Name("Finished").Status(enttenant.StatusDone).Deleted().Create()
	te.clearEvents(ctx)

	require.True(t, execOK[restoreTenantData](te, ctx, restoreTenantMut, tenantArgs(tenantID, nil)).RestoreTenant.Success)

	got := mustGetTenant(t, te, tenantID)
	assert.True(t, got.DeletedAt.IsZero(), "a done tenant comes back")
	te.assertEvents(ctx, Update("tenant", tenantID))
}

// The expiry rule is checked before the row is read, so the refusal a
// client gets for a past timestamp does not depend on the target's state,
// and cannot be used to probe whether a tenant id exists.
func TestRestoreTenant_RefusesAPastExpiryBeforeReadingTheRow(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	unknown := uuidgql.GenerateV7UUID()
	ctx := te.ctxForTenant(systemUser, unknown)

	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	execErr(te, ctx, restoreTenantWithExpiryMut, tenantArgs(unknown, map[string]any{
		"ExpiresAt": past,
	}), "expiresAt must be in the future")
}

// restoreTenant names its target too, so a tenant admin must not reach a
// foreign tenant with it. The role gate answers before the system-caller
// gate, and both have to hold.
func TestRestoreTenant_RejectsACrossTenantTarget(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Name("CallerTenant").Create()
	te.newTenant(te.ctxForTenant(systemUser, resolver.TenantB), resolver.TenantB).
		Name("ForeignDisabled").Status(enttenant.StatusDone).Deleted().Create()

	execErr(te, te.ctx(userA), restoreTenantMut, tenantArgs(resolver.TenantB, nil), "admin role required")

	got := mustGetTenant(t, te, resolver.TenantB)
	assert.False(t, got.DeletedAt.IsZero(), "the foreign tenant must stay disabled")
}

// The restore is audited like the delete: the row's deleted_by is cleared
// and the event names the operator who brought the tenant back.
func TestRestoreTenant_RecordsTheActingOperator(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	tenantID := uuidgql.GenerateV7UUID()
	ctx := te.ctxForTenant(systemUser, tenantID)
	te.newTenant(ctx, tenantID).Name("Restored").Status(enttenant.StatusDone).Deleted().Create()
	te.clearEvents(ctx)

	require.True(t, execOK[restoreTenantData](te, ctx, restoreTenantMut, tenantArgs(tenantID, nil)).RestoreTenant.Success)

	got := mustGetTenant(t, te, tenantID)
	assert.Equal(t, uuid.Nil, got.DeletedBy, "the restore clears the disabling user")

	rows := outboxRows(t, te, ctx)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].UserID, "the outbox row must carry the acting user")
	assert.Equal(t, systemUser.ID, *rows[0].UserID)
	assert.Equal(t, tenantID, rows[0].TenantID)
}

// =============================================================================
// setTenantExpiry: the target is the input too
// =============================================================================

// setTenantExpiry keeps its system-caller gate, but the role gate runs
// first and is now against the named target: a tenant admin naming a
// foreign tenant is refused on the role, never reaching the expiry column.
func TestSetTenantExpiry_RejectsACrossTenantTarget(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	expiry := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Name("CallerTenant").Create()
	te.newTenant(te.ctxForTenant(systemUser, resolver.TenantB), resolver.TenantB).
		Name("ForeignTenant").ExpiresAt(expiry).Create()

	execErr(te, te.ctx(userA), clearTenantExpiryMut, tenantArgs(resolver.TenantB, nil), "admin role required")

	got := mustGetTenant(t, te, resolver.TenantB)
	require.NotNil(t, got.ExpiresAt, "the foreign tenant's expiry must survive a refused call")
	assert.Equal(t, expiry, got.ExpiresAt.UTC())
}

// An operator may name any tenant: the system caller passes every role
// check, which is what makes the explicit target usable for operations at
// all. The event is topic-scoped to the target, not to the caller's header.
func TestSetTenantExpiry_OperatorWritesTheNamedTenant(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	target := uuidgql.GenerateV7UUID()
	// The request's own tenant header names a different tenant than the
	// mutation's target, which is the whole point of the argument.
	header := uuidgql.GenerateV7UUID()
	te.newTenant(te.ctxForTenant(systemUser, target), target).Name("Target").Create()
	te.newTenant(te.ctxForTenant(systemUser, header), header).Name("Header").Create()

	ctx := te.ctxForTenant(systemUser, header)
	te.clearEvents(ctx)

	expiry := time.Date(2099, 6, 1, 0, 0, 0, 0, time.UTC)
	data := execOK[setTenantExpiryData](te, ctx, setTenantExpiryMut, tenantArgs(target, map[string]any{
		"ExpiresAt": expiry.Format(time.RFC3339),
	}))
	require.True(t, data.SetTenantExpiry.Success)

	got := mustGetTenant(t, te, target)
	require.NotNil(t, got.ExpiresAt)
	assert.Equal(t, expiry, got.ExpiresAt.UTC(), "the named tenant is the one that is written")

	untouched := mustGetTenant(t, te, header)
	assert.Nil(t, untouched.ExpiresAt, "the caller's own tenant must not be written instead")

	rows := outboxRows(t, te, ctx)
	require.Len(t, rows, 1)
	assert.Equal(t, target, rows[0].TenantID, "the event is scoped to the target tenant")
}

// An anonymous caller is refused before anything else: the target argument
// must not become a way to reach a tenant without a token.
func TestTenantLifecycle_AnonymousCallerIsRefusedOnEveryMutation(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	tenantID := uuidgql.GenerateV7UUID()
	te.newTenant(te.ctxForTenant(systemUser, tenantID), tenantID).Name("Untouchable").Status(enttenant.StatusDone).Create()

	anonCtx := te.ctx(&authn.User{})
	// A plain loop, not subtests: the environment is closed when this
	// function returns, which parallel subtests would outlive. The row is
	// re-read after every case so a regression names the mutation that
	// wrote it.
	mutations := []struct {
		name string
		tpl  resolver.Template
	}{
		{"deleteTenant", deleteTenantMut},
		{"restoreTenant", restoreTenantMut},
		{"setTenantExpiry", clearTenantExpiryMut},
	}
	for _, m := range mutations {
		execErr(te, anonCtx, m.tpl, tenantArgs(tenantID, nil), "authentication required")

		got := mustGetTenant(t, te, tenantID)
		assert.True(t, got.DeletedAt.IsZero(), "%s must not retire a tenant for an anonymous caller", m.name)
		assert.Nil(t, got.ExpiresAt, "%s must not write an expiry for an anonymous caller", m.name)
		assert.Equal(t, enttenant.StatusDone, got.Status, "%s must not touch the row at all", m.name)
	}
}
