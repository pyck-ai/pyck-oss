package registertenant_test

import (
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	commonwf "github.com/pyck-ai/pyck/backend/common/workflow"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

// Re-running CreateTenantInDbActivity with a new ExpiresAt for an
// already-existing tenant ID must land the new value, not silently keep
// the old one. Pre-fix the activity wrote
// `INSERT … ON CONFLICT(id) DO NOTHING`, so the second call was a
// no-op against the row's existing expiry — the operator-supplied T2
// was silently dropped and the periodic expiry sweep fired at the
// stale T1.
func TestCreateTenantInDbActivity_RewritesExpiresAtOnConflict(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { _ = client.Close() })

	const audience = "test-audience"
	const orgID = "org-rewrite"

	nsGetter := commonwf.NewNamespaceGetter(audience)
	a := registertenant.NewActivities(nil, client, nil, nsGetter)

	ctx := authn.Context(context.Background(), authn.SystemUser())

	t1 := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)

	// First call: row created with T1.
	require.NoError(t, a.CreateTenantInDbActivity(ctx, registertenant.CreateTenantInDbActivityInput{
		OrganizationID: orgID,
		Name:           "Tenant Rewrite",
		ExpiresAt:      &t1,
	}))

	tenantID := nsGetter.GetTenantID(orgID)

	got, err := client.Tenant.Get(ctx, tenantID)
	require.NoError(t, err)
	require.NotNil(t, got.ExpiresAt)
	assert.True(t, got.ExpiresAt.Equal(t1), "first call must set ExpiresAt = T1")

	// Second call: same OrganizationID, but ExpiresAt = T2. Pre-fix
	// `DO NOTHING` kept T1; post-fix the conflict path updates the
	// expiry column to T2.
	require.NoError(t, a.CreateTenantInDbActivity(ctx, registertenant.CreateTenantInDbActivityInput{
		OrganizationID: orgID,
		Name:           "Tenant Rewrite",
		ExpiresAt:      &t2,
	}))

	got, err = client.Tenant.Get(ctx, tenantID)
	require.NoError(t, err)
	require.NotNil(t, got.ExpiresAt)
	assert.True(t, got.ExpiresAt.Equal(t2), "re-create with new ExpiresAt must overwrite T1 with T2; got %v", got.ExpiresAt)
}

// Passing nil ExpiresAt on re-create must clear the previously-set value.
// Symmetric with the rewrite case: idempotency means "land the caller's
// intent", not "keep whatever was there first".
func TestCreateTenantInDbActivity_ClearsExpiresAtOnConflictWhenNil(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { _ = client.Close() })

	const audience = "test-audience"
	const orgID = "org-clear"

	nsGetter := commonwf.NewNamespaceGetter(audience)
	a := registertenant.NewActivities(nil, client, nil, nsGetter)

	ctx := authn.Context(context.Background(), authn.SystemUser())
	ctx = feature.Context(ctx, feature.FEATURE_SHOW_DELETED)

	t1 := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, a.CreateTenantInDbActivity(ctx, registertenant.CreateTenantInDbActivityInput{
		OrganizationID: orgID,
		Name:           "Tenant Clear",
		ExpiresAt:      &t1,
	}))

	require.NoError(t, a.CreateTenantInDbActivity(ctx, registertenant.CreateTenantInDbActivityInput{
		OrganizationID: orgID,
		Name:           "Tenant Clear",
		ExpiresAt:      nil,
	}))

	tenantID := nsGetter.GetTenantID(orgID)
	got, err := client.Tenant.Get(ctx, tenantID)
	require.NoError(t, err)
	assert.Nil(t, got.ExpiresAt, "re-create with ExpiresAt=nil must clear the prior value")
}

// Re-registering an org whose tenant row was soft-deleted must
// resurrect it — clear deleted_at/deleted_by so the row is active
// again. The id is deterministic (ComputeUUID over the org), so the
// conflict path lands on the prior soft-deleted row. Pre-fix the
// upsert rewrote expires_at/data but left deleted_at set, so the
// activity reported success while the tenant stayed soft-deleted
// (invisible to the query filter, Zitadel org still INACTIVE) — a
// ghost.
func TestCreateTenantInDbActivity_ResurrectsSoftDeletedRowOnConflict(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { _ = client.Close() })

	const audience = "test-audience"
	const orgID = "org-resurrect"

	nsGetter := commonwf.NewNamespaceGetter(audience)
	a := registertenant.NewActivities(nil, client, nil, nsGetter)

	// SHOW_DELETED so the post-conflict Get can observe the row's
	// deleted_at regardless of state.
	ctx := authn.Context(context.Background(), authn.SystemUser())
	ctx = feature.Context(ctx, feature.FEATURE_SHOW_DELETED)

	t1 := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

	// First call creates the row.
	require.NoError(t, a.CreateTenantInDbActivity(ctx, registertenant.CreateTenantInDbActivityInput{
		OrganizationID: orgID,
		Name:           "Tenant Resurrect",
		ExpiresAt:      &t1,
	}))

	tenantID := nsGetter.GetTenantID(orgID)

	// Soft-delete it (the row is currently active, so the mutation
	// filter permits the update).
	require.NoError(t, client.Tenant.UpdateOneID(tenantID).
		SetDeletedAt(time.Now().UTC()).
		SetDeletedBy(uuid.Max).
		Exec(ctx))

	got, err := client.Tenant.Get(ctx, tenantID)
	require.NoError(t, err)
	require.False(t, got.DeletedAt.IsZero(), "precondition: row must be soft-deleted")

	// Re-register the same org: the conflict path must resurrect it.
	t2 := time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)
	require.NoError(t, a.CreateTenantInDbActivity(ctx, registertenant.CreateTenantInDbActivityInput{
		OrganizationID: orgID,
		Name:           "Tenant Resurrect",
		ExpiresAt:      &t2,
	}))

	got, err = client.Tenant.Get(ctx, tenantID)
	require.NoError(t, err)
	assert.True(t, got.DeletedAt.IsZero(), "re-register must clear deleted_at (resurrect), not leave a ghost")
	assert.Equal(t, uuid.Nil, got.DeletedBy, "re-register must clear deleted_by")
	require.NotNil(t, got.ExpiresAt)
	assert.True(t, got.ExpiresAt.Equal(t2), "re-register must land the new expiry on the resurrected row")
}
