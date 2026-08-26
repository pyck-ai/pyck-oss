package resolvers_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"

	"github.com/pyck-ai/pyck/backend/management/core"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	registertenantwf "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var registerTenant = resolver.ParseTemplate(`mutation {
	registerTenant(input: {
		name: "{{.Name}}"
		adminUsername: "{{.AdminUsername}}"
		adminEmail: "{{.AdminEmail}}"
		adminFirstName: "{{.AdminFirstName}}"
		adminLastName: "{{.AdminLastName}}"
		adminPassword: "{{.AdminPassword}}"
	}) {
		success
	}
}`)

var registerTenantWithData = resolver.ParseTemplate(`mutation {
	registerTenant(input: {
		name: "{{.Name}}"
		adminUsername: "{{.AdminUsername}}"
		adminEmail: "{{.AdminEmail}}"
		adminFirstName: "{{.AdminFirstName}}"
		adminLastName: "{{.AdminLastName}}"
		adminPassword: "{{.AdminPassword}}"
		data: {flavour: "{{.Flavour}}"}
	}) {
		success
	}
}`)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type (
	registerTenantData struct {
		RegisterTenant struct {
			Success bool
		}
	}
	deleteTenantData struct {
		DeleteTenant struct{ Success bool }
	}

	restoreTenantData struct {
		RestoreTenant struct{ Success bool }
	}
	setTenantExpiryData struct {
		SetTenantExpiry struct{ Success bool }
	}
	setTenantUITemplateData struct {
		SetTenantUITemplate struct{ Success bool }
	}
)

// =============================================================================
// REGISTER TENANT TESTS
// =============================================================================

func TestRegisterTenant(t *testing.T) {
	t.Parallel()

	t.Run("registers tenant successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		newTenantID := uuid.New()
		workflowOutput := registertenantwf.RegisterTenantWorkflowOutput{
			OrganizationID: "org-123",
			TenantID:       newTenantID,
			LoginName:      "testadmin",
			UserID:         uuid.New().String(),
			UserRoles:      []string{"admin"},
		}

		// Simulate the workflow's CreateTenantInDbActivity side-effect by
		// seeding the row when ExecuteWorkflow is invoked. Seeding earlier
		// would trip the resolver's name-uniqueness pre-flight check;
		// seeding here means the row exists by the time the resolver does
		// its post-workflow Tenant.Get.
		mockRun := mocks.NewMockWorkflowRun("workflow-id", "run-id", workflowOutput, nil)
		te.TemporalClient.
			On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(_ mock.Arguments) {
				//nolint:contextcheck // Test seed runs inside a mock callback; reusing the test ctx is intentional.
				te.newTenant(ctx, newTenantID).Name("Test Tenant").Create()
			}).
			Return(mockRun, nil).Once()
		te.TemporalClient.
			On("GetWorkflow", mock.Anything, "workflow-id", "run-id").
			Return(mockRun).Once()

		data := execOK[registerTenantData](te, ctx, registerTenant, map[string]any{
			"Name":           "Test Tenant",
			"AdminUsername":  "testadmin",
			"AdminEmail":     "testadmin@example.com",
			"AdminFirstName": "Test",
			"AdminLastName":  "Admin",
			"AdminPassword":  "SecurePass123!",
		})

		assert.True(t, data.RegisterTenant.Success)

		// The seeded Tenant.Create emits a single management.tenant.create
		// outbox event (mirroring what the workflow's CreateTenantInDbActivity
		// would emit in production). The resolver itself produces no
		// additional synchronous events.
		te.assertEvents(ctx, Create("tenant", newTenantID))
	})

	t.Run("passes flavour to workflow input", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		newTenantID := uuid.New()
		workflowOutput := registertenantwf.RegisterTenantWorkflowOutput{
			OrganizationID: "org-123",
			TenantID:       newTenantID,
			LoginName:      "testadmin",
			UserID:         uuid.New().String(),
			UserRoles:      []string{"admin"},
		}

		// Seed the row when ExecuteWorkflow fires (after the resolver's
		// uniqueness pre-flight, before the post-workflow Tenant.Get).
		mockRun := mocks.NewMockWorkflowRun("workflow-id", "run-id", workflowOutput, nil)
		te.TemporalClient.
			On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.MatchedBy(func(args []interface{}) bool {
				if len(args) != 1 {
					return false
				}
				input, ok := args[0].(registertenantwf.RegisterTenantWorkflowInput)
				return ok && input.Data["flavour"] == "pyck-go"
			})).
			Run(func(_ mock.Arguments) {
				//nolint:contextcheck // Test seed runs inside a mock callback; reusing the test ctx is intentional.
				te.newTenant(ctx, newTenantID).Name("Test Tenant").Create()
			}).
			Return(mockRun, nil).Once()
		te.TemporalClient.
			On("GetWorkflow", mock.Anything, "workflow-id", "run-id").
			Return(mockRun).Once()

		data := execOK[registerTenantData](te, ctx, registerTenantWithData, map[string]any{
			"Name":           "Test Tenant",
			"AdminUsername":  "testadmin",
			"AdminEmail":     "testadmin@example.com",
			"AdminFirstName": "Test",
			"AdminLastName":  "Admin",
			"AdminPassword":  "SecurePass123!",
			"Flavour":        "pyck-go",
		})

		assert.True(t, data.RegisterTenant.Success)
		te.TemporalClient.AssertExpectations(t)
	})
}

// A failed registration must not destroy an unrelated, previously
// soft-deleted tenant that merely shares the display name. The rollback
// looks the cleanup target up by `tenant.Name(input.Name)`, and `name`
// is NOT unique (only idp_org_ref is). The lookup therefore stays on
// systemCtx — NOT FEATURE_SHOW_DELETED — so it can only ever match an
// ACTIVE row this registration created. The RegisterTenantWorkflow's own
// saga rollback owns soft-deleting the row it created (by deterministic
// id); the resolver never needs to see or hard-delete a soft-deleted row.
//
// Pre-revert the lookup ran under FEATURE_SHOW_DELETED + entprivacy.Allow,
// so it matched the unrelated soft-deleted tenant and hard-deleted it —
// data loss on a failed registration. This test fails on that code.
func TestRegisterTenantRollback_DoesNotDestroyUnrelatedSoftDeletedTenant(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)
	ctx := te.ctx(userA)

	// An unrelated tenant, same display name, already soft-deleted.
	// Invisible to the resolver's active-name pre-flight (systemCtx), so
	// registration proceeds.
	unrelatedID := uuidgql.GenerateV7UUID()
	te.newTenant(te.ctxForTenant(systemUser, unrelatedID), unrelatedID).
		Name("Acme").Deleted().Create()

	seeded, seedErr := te.Ent.Tenant.Query().
		Where(enttenant.IDEQ(unrelatedID)).
		Exist(te.ctxWithDeleted(systemUser))
	require.NoError(t, seedErr)
	require.True(t, seeded, "precondition: unrelated soft-deleted tenant must be seeded")

	// The provisioning workflow fails (its own saga has already cleaned
	// up whatever it created).
	wfErr := errors.New("provisioning boom")
	mockRun := mocks.NewMockWorkflowRun("workflow-id", "run-id", nil, wfErr)
	te.TemporalClient.
		On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(mockRun, nil).Once()
	te.TemporalClient.
		On("GetWorkflow", mock.Anything, "workflow-id", "run-id").
		Return(mockRun).Once()

	// The real workflow error surfaces (not a masked NotFound, not a
	// NotSingular from matching two same-named rows).
	execErr(te, ctx, registerTenant, map[string]any{
		"Name":           "Acme",
		"AdminUsername":  "acmeadmin",
		"AdminEmail":     "acme@example.com",
		"AdminFirstName": "Acme",
		"AdminLastName":  "Admin",
		"AdminPassword":  "SecurePass123!",
	}, "provisioning boom")

	// The unrelated soft-deleted tenant must be untouched.
	exists, err := te.Ent.Tenant.Query().
		Where(enttenant.IDEQ(unrelatedID)).
		Exist(te.ctxWithDeleted(systemUser))
	require.NoError(t, err)
	assert.True(t, exists, "rollback must not hard-delete an unrelated same-named soft-deleted tenant")
}

// =============================================================================
// TENANT LIFECYCLE TEMPLATES (disable / restore / setExpiry)
// =============================================================================

var (
	deleteTenantMut = resolver.ParseTemplate(`mutation {
		deleteTenant {
			success
		}
	}`)

	restoreTenantMut = resolver.ParseTemplate(`mutation {
		restoreTenant(input: {}) {
			success
		}
	}`)

	restoreTenantWithExpiryMut = resolver.ParseTemplate(`mutation {
		restoreTenant(input: { expiresAt: "{{.ExpiresAt}}" }) {
			success
		}
	}`)

	setTenantExpiryMut = resolver.ParseTemplate(`mutation {
		setTenantExpiry(input: { expiresAt: "{{.ExpiresAt}}" }) {
			success
		}
	}`)

	clearTenantExpiryMut = resolver.ParseTemplate(`mutation {
		setTenantExpiry(input: { expiresAt: null }) {
			success
		}
	}`)

	setTenantUITemplateMut = resolver.ParseTemplate(`mutation {
		setTenantUITemplate(input: { webTemplate: "{{.Web}}", mobileTemplate: "{{.Mobile}}" }) {
			success
		}
	}`)

	setTenantUITemplateWebOnlyMut = resolver.ParseTemplate(`mutation {
		setTenantUITemplate(input: { webTemplate: "{{.Web}}" }) {
			success
		}
	}`)

	setTenantUITemplateEmptyMut = resolver.ParseTemplate(`mutation {
		setTenantUITemplate(input: {}) {
			success
		}
	}`)

	setTenantUITemplateClearWebMut = resolver.ParseTemplate(`mutation {
		setTenantUITemplate(input: { clearWebTemplate: true }) {
			success
		}
	}`)

	setTenantUITemplateSetAndClearMut = resolver.ParseTemplate(`mutation {
		setTenantUITemplate(input: { webTemplate: "{{.Web}}", clearWebTemplate: true }) {
			success
		}
	}`)
)

// =============================================================================
// DISABLE TENANT
// =============================================================================

func TestDeleteTenant(t *testing.T) {
	t.Parallel()

	t.Run("soft-deletes an active tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Name("ToDelete").Create()
		te.clearEvents(ctx)

		data := execOK[deleteTenantData](te, ctx, deleteTenantMut, nil)
		assert.True(t, data.DeleteTenant.Success)

		got := mustGetTenant(t, te, tenantID)
		assert.False(t, got.DeletedAt.IsZero(), "deleted_at should be set")

		te.assertEvents(ctx, Delete("tenant", tenantID))
	})

	t.Run("is idempotent on an already-deleted tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Name("AlreadyOff").Deleted().Create()
		te.clearEvents(ctx)

		data := execOK[deleteTenantData](te, ctx, deleteTenantMut, nil)
		assert.True(t, data.DeleteTenant.Success)

		te.assertNoEvents(ctx)
	})

	t.Run("errors on unknown tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctxForTenant(systemUser, uuidgql.GenerateV7UUID())

		execErr(te, ctx, deleteTenantMut, nil, "query tenant")
	})

	t.Run("rejects unauthenticated callers", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		te.newTenant(te.ctxForTenant(systemUser, tenantID), tenantID).Create()

		// Anonymous request: no X-Pyck-Tenant-Id header (user.TenantID is
		// uuid.Nil → SendQuery skips the header), so tenant.HTTPMiddleware
		// doesn't reject the request and the resolver's IsAuthenticated()
		// check is what surfaces the error.
		anonCtx := te.ctx(&authn.User{})
		execErr(te, anonCtx, deleteTenantMut, nil, "authentication required")
	})

	// Regression test for M1 (PR #1172 round-2 review): authenticated-but-
	// non-admin callers must be rejected before the resolver swaps to the
	// system user. Without the role gate, any writer/reader of the target
	// tenant — or, once paired with the schema change, any authenticated
	// user of ANY tenant — could trigger the disable workflow.
	t.Run("rejects non-admin caller (M1: role gate)", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// userAWriter is a writer on TenantA, not an admin. te.ctx sets
		// MutationTenantID = TenantA, so this exercises "caller is in the
		// target tenant but lacks ROLE_ADMIN" — the closest analogue of
		// the cross-tenant attack at the unit-test layer.
		te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Name("VictimTenant").Create()

		writerCtx := te.ctx(userAWriter)
		execErr(te, writerCtx, deleteTenantMut, nil, "admin role required")
	})
}

// =============================================================================
// RESTORE TENANT
// =============================================================================

func TestRestoreTenant(t *testing.T) {
	t.Parallel()

	t.Run("clears deleted_at and preserves prior expiry when none supplied", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		priorExpiry := time.Date(2099, 6, 1, 0, 0, 0, 0, time.UTC)
		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).ExpiresAt(priorExpiry).Deleted().Create()
		te.clearEvents(ctx)

		data := execOK[restoreTenantData](te, ctx, restoreTenantMut, nil)
		assert.True(t, data.RestoreTenant.Success)

		got := mustGetTenant(t, te, tenantID)
		assert.True(t, got.DeletedAt.IsZero(), "deleted_at should be cleared")
		require.NotNil(t, got.ExpiresAt, "prior expiry should be preserved")
		assert.True(t, got.ExpiresAt.Equal(priorExpiry), "prior expiry should be unchanged")

		te.assertEvents(ctx, Update("tenant", tenantID))
	})

	t.Run("overrides expiry when input.expiresAt is supplied", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		oldExpiry := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		newExpiry := time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).ExpiresAt(oldExpiry).Deleted().Create()
		te.clearEvents(ctx)

		data := execOK[restoreTenantData](te, ctx, restoreTenantWithExpiryMut, map[string]any{
			"ExpiresAt": newExpiry.Format(time.RFC3339),
		})
		assert.True(t, data.RestoreTenant.Success)

		got := mustGetTenant(t, te, tenantID)
		assert.True(t, got.DeletedAt.IsZero())
		require.NotNil(t, got.ExpiresAt)
		assert.True(t, got.ExpiresAt.Equal(newExpiry), "expiry should be overwritten with new value")
	})

	t.Run("is idempotent on an already-active tenant and ignores input.expiresAt", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		priorExpiry := time.Date(2099, 6, 1, 0, 0, 0, 0, time.UTC)
		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).ExpiresAt(priorExpiry).Create()
		te.clearEvents(ctx)

		data := execOK[restoreTenantData](te, ctx, restoreTenantWithExpiryMut, map[string]any{
			"ExpiresAt": "2050-01-01T00:00:00Z",
		})
		assert.True(t, data.RestoreTenant.Success)

		got := mustGetTenant(t, te, tenantID)
		require.NotNil(t, got.ExpiresAt)
		assert.True(t, got.ExpiresAt.Equal(priorExpiry), "expiresAt arg must be ignored on already-active path")

		te.assertNoEvents(ctx)
	})

	// Pre-fix the resolver accepted any timestamp for input.expiresAt,
	// including values already in the past. The tenant got restored, the
	// Zitadel org reactivated, and then the periodic expiry sweep re-
	// disabled it within ~60s — operator saw a brief restore-then-vanish
	// with no error. Post-fix a past timestamp returns a typed error
	// before any side effects fire.
	t.Run("rejects past expiresAt", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Deleted().Create()
		te.clearEvents(ctx)

		past := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
		execErr(te, ctx, restoreTenantWithExpiryMut, map[string]any{
			"ExpiresAt": past,
		}, "expiresAt must be in the future")

		// Tenant must still be deleted — restore did NOT fire.
		got := mustGetTenant(t, te, tenantID)
		assert.False(t, got.DeletedAt.IsZero(), "tenant must remain soft-deleted after a rejected restore")
		te.assertNoEvents(ctx)
	})

	t.Run("errors on unknown tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctxForTenant(systemUser, uuidgql.GenerateV7UUID())

		execErr(te, ctx, restoreTenantMut, nil, "query tenant")
	})

	t.Run("rejects unauthenticated callers", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		te.newTenant(te.ctxForTenant(systemUser, tenantID), tenantID).Deleted().Create()

		// See TestDeleteTenant/rejects_unauthenticated_callers for why
		// te.ctx (not ctxForTenant) is the right shape here.
		anonCtx := te.ctx(&authn.User{})
		execErr(te, anonCtx, restoreTenantMut, nil, "authentication required")
	})

	// Regression test for M1: see TestDeleteTenant for the same shape.
	t.Run("rejects non-admin caller (M1: role gate)", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Name("DormantTenant").Deleted().Create()

		writerCtx := te.ctx(userAWriter)
		execErr(te, writerCtx, restoreTenantMut, nil, "admin role required")
	})
}

// =============================================================================
// SET TENANT EXPIRY
// =============================================================================

func TestSetTenantExpiry(t *testing.T) {
	t.Parallel()

	t.Run("sets expiry on an active tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Create()
		te.clearEvents(ctx)

		newExpiry := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		data := execOK[setTenantExpiryData](te, ctx, setTenantExpiryMut, map[string]any{
			"ExpiresAt": newExpiry.Format(time.RFC3339),
		})
		assert.True(t, data.SetTenantExpiry.Success)

		got := mustGetTenant(t, te, tenantID)
		require.NotNil(t, got.ExpiresAt)
		assert.True(t, got.ExpiresAt.Equal(newExpiry))

		te.assertEvents(ctx, Update("tenant", tenantID))
	})

	t.Run("clears expiry when input.expiresAt is null", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).ExpiresAt(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)).Create()
		te.clearEvents(ctx)

		data := execOK[setTenantExpiryData](te, ctx, clearTenantExpiryMut, nil)
		assert.True(t, data.SetTenantExpiry.Success)

		got := mustGetTenant(t, te, tenantID)
		assert.Nil(t, got.ExpiresAt)
	})

	t.Run("is idempotent when the value already matches", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		expiry := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).ExpiresAt(expiry).Create()
		te.clearEvents(ctx)

		data := execOK[setTenantExpiryData](te, ctx, setTenantExpiryMut, map[string]any{
			"ExpiresAt": expiry.Format(time.RFC3339),
		})
		assert.True(t, data.SetTenantExpiry.Success)

		te.assertNoEvents(ctx)
	})

	// Pre-fix the equality check used Truncate(time.Second) on both sides.
	// Two values differing only at sub-second precision compared equal and
	// the resolver short-circuited Success without executing an Update —
	// no outbox event, no cache eviction, the caller's new value silently
	// lost. Post-fix any difference (even sub-second) goes through.
	// RFC3339 lacks sub-second precision, so the test uses
	// RFC3339Nano via a custom mutation that takes the raw string.
	t.Run("sub-second change fires the update", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		stored := time.Date(2099, 1, 1, 12, 34, 56, 300*int(time.Millisecond), time.UTC)
		desired := time.Date(2099, 1, 1, 12, 34, 56, 500*int(time.Millisecond), time.UTC)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).ExpiresAt(stored).Create()
		te.clearEvents(ctx)

		data := execOK[setTenantExpiryData](te, ctx, setTenantExpiryMut, map[string]any{
			"ExpiresAt": desired.Format(time.RFC3339Nano),
		})
		assert.True(t, data.SetTenantExpiry.Success)

		got := mustGetTenant(t, te, tenantID)
		require.NotNil(t, got.ExpiresAt)
		assert.True(t, got.ExpiresAt.Equal(desired), "sub-second change must land on the row (got %v, want %v)", got.ExpiresAt, desired)
		te.assertEvents(ctx, Update("tenant", tenantID))
	})

	t.Run("errors on unknown tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctxForTenant(systemUser, uuidgql.GenerateV7UUID())

		execErr(te, ctx, clearTenantExpiryMut, nil, "query tenant")
	})

	t.Run("errors on a soft-deleted tenant (invisible to this resolver)", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Deleted().Create()
		te.clearEvents(ctx)

		execErr(te, ctx, clearTenantExpiryMut, nil, "query tenant")
	})

	t.Run("rejects unauthenticated callers", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		te.newTenant(te.ctxForTenant(systemUser, tenantID), tenantID).Create()

		// See TestDeleteTenant/rejects_unauthenticated_callers for why
		// te.ctx (not ctxForTenant) is the right shape here.
		anonCtx := te.ctx(&authn.User{})
		execErr(te, anonCtx, clearTenantExpiryMut, nil, "authentication required")
	})

	// Regression test for M1: see TestDeleteTenant for the same shape.
	t.Run("rejects non-admin caller (M1: role gate)", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Name("ExpiryTarget").Create()

		writerCtx := te.ctx(userAWriter)
		execErr(te, writerCtx, clearTenantExpiryMut, nil, "admin role required")
	})
}

// mustGetTenant reads a tenant including soft-deleted rows.
// =============================================================================
// SET TENANT UI TEMPLATE
// =============================================================================

func TestSetTenantUITemplate(t *testing.T) {
	t.Parallel()

	const (
		webKey     = core.RemoteWebUITemplateKey
		mobileKey  = core.RemoteMobileUITemplateKey
		webTmpl    = "https://cdn.example.com/t/web/{{.Slug}}/{{.Version}}/mf-manifest.json"
		mobileTmpl = "https://cdn.example.com/t/mobile/{{.Slug}}/{{.Version}}/widgets.rfw"
	)

	t.Run("system user sets both templates", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Create()
		te.clearEvents(ctx)

		data := execOK[setTenantUITemplateData](te, ctx, setTenantUITemplateMut, map[string]any{
			"Web": webTmpl, "Mobile": mobileTmpl,
		})
		assert.True(t, data.SetTenantUITemplate.Success)

		got := mustGetTenant(t, te, tenantID)
		assert.Equal(t, webTmpl, got.Data[webKey])
		assert.Equal(t, mobileTmpl, got.Data[mobileKey])

		te.assertEvents(ctx, Update("tenant", tenantID))
	})

	t.Run("writes only the provided field, leaving the other unchanged", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Data(map[string]any{mobileKey: "keep-mobile"}).Create()
		te.clearEvents(ctx)

		data := execOK[setTenantUITemplateData](te, ctx, setTenantUITemplateWebOnlyMut, map[string]any{
			"Web": webTmpl,
		})
		assert.True(t, data.SetTenantUITemplate.Success)

		got := mustGetTenant(t, te, tenantID)
		assert.Equal(t, webTmpl, got.Data[webKey])
		assert.Equal(t, "keep-mobile", got.Data[mobileKey])
	})

	t.Run("is idempotent when the values already match", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Data(map[string]any{webKey: webTmpl, mobileKey: mobileTmpl}).Create()
		te.clearEvents(ctx)

		data := execOK[setTenantUITemplateData](te, ctx, setTenantUITemplateMut, map[string]any{
			"Web": webTmpl, "Mobile": mobileTmpl,
		})
		assert.True(t, data.SetTenantUITemplate.Success)

		te.assertNoEvents(ctx)
	})

	t.Run("rejects a non-system (admin) caller", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		te.newTenant(te.ctxForTenant(systemUser, resolver.TenantA), resolver.TenantA).Create()

		adminCtx := te.ctx(userA)
		execErr(te, adminCtx, setTenantUITemplateMut, map[string]any{
			"Web": webTmpl, "Mobile": mobileTmpl,
		}, "system role required")
	})

	t.Run("rejects an unauthenticated caller", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		anonCtx := te.ctx(&authn.User{})
		execErr(te, anonCtx, setTenantUITemplateMut, map[string]any{
			"Web": webTmpl, "Mobile": mobileTmpl,
		}, "system role required")
	})

	t.Run("rejects a request that changes nothing", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Create()

		execErr(te, ctx, setTenantUITemplateEmptyMut, nil, "at least one")
	})

	t.Run("rejects a malformed template", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Create()

		// Missing the {{.Version}} placeholder.
		execErr(te, ctx, setTenantUITemplateWebOnlyMut, map[string]any{
			"Web": "https://cdn.example.com/web/{{.Slug}}/mf-manifest.json",
		}, "invalid UI bundle URL template")
	})

	t.Run("clears a stored template, reverting to the default", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Data(map[string]any{webKey: webTmpl, mobileKey: mobileTmpl}).Create()
		te.clearEvents(ctx)

		data := execOK[setTenantUITemplateData](te, ctx, setTenantUITemplateClearWebMut, nil)
		assert.True(t, data.SetTenantUITemplate.Success)

		got := mustGetTenant(t, te, tenantID)
		_, webPresent := got.Data[webKey]
		assert.False(t, webPresent, "web template should be deleted, not blanked")
		assert.Equal(t, mobileTmpl, got.Data[mobileKey], "mobile template untouched")

		te.assertEvents(ctx, Update("tenant", tenantID))
	})

	t.Run("rejects setting and clearing the same template", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		tenantID := uuidgql.GenerateV7UUID()
		ctx := te.ctxForTenant(systemUser, tenantID)
		te.newTenant(ctx, tenantID).Create()

		execErr(te, ctx, setTenantUITemplateSetAndClearMut, map[string]any{
			"Web": webTmpl,
		}, "cannot set and clear")
	})
}

func mustGetTenant(t *testing.T, te *testEnv, id uuid.UUID) *ent.Tenant {
	t.Helper()
	tenant, err := te.Ent.Tenant.Get(te.ctxWithDeleted(systemUser), id)
	require.NoError(t, err)
	return tenant
}
