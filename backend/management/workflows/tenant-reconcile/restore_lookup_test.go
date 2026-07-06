package tenantreconcile_test

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
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
)

// The reconciler's restore-side lookup runs under FEATURE_SHOW_DELETED
// (the activity opens that ctx so it can see disabled rows on the
// other branch). Without an explicit `DeletedAtIsNil()` predicate the
// query returns soft-deleted rows too — a soft-delete that lands
// between the disabled-snapshot and the restore-side query is silently
// added to ToRestore, and the reconciler then dispatches
// RestoreTenantWorkflow against the admin's most-recent intent.
//
// This test bypasses the Zitadel side and exercises the predicate
// directly: under SHOW_DELETED, the predicate WITH DeletedAtIsNil
// excludes soft-deleted rows; without it, they leak through.
func TestRestoreSideLookupExcludesSoftDeleted(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { _ = client.Close() })

	sysCtx := authn.Context(context.Background(), authn.SystemUser())
	sysCtx = feature.Context(sysCtx, feature.FEATURE_SHOW_DELETED)

	// Seed: one active tenant, one soft-deleted tenant. Both have
	// idp_org_ref values that the restore-side lookup would consider.
	activeID := uuid.New()
	deletedID := uuid.New()
	now := time.Now().UTC()

	_, err := client.Tenant.Create().
		SetID(activeID).
		SetName("active").
		SetIdpOrgRef("org-active").
		Save(sysCtx)
	require.NoError(t, err)

	_, err = client.Tenant.Create().
		SetID(deletedID).
		SetName("deleted").
		SetIdpOrgRef("org-deleted").
		SetDeletedAt(now).
		Save(sysCtx)
	require.NoError(t, err)

	// The activity's restore-side query without the fix: would return
	// BOTH rows under FEATURE_SHOW_DELETED.
	allRows, err := client.Tenant.Query().
		Where(enttenant.IdpOrgRefIn("org-active", "org-deleted")).
		Limit(mixin.Limit).
		All(sysCtx)
	require.NoError(t, err)
	assert.Len(t, allRows, 2, "baseline: SHOW_DELETED returns soft-deleted rows when DeletedAtIsNil is omitted")

	// The activity's restore-side query WITH the fix: excludes the
	// soft-deleted row even under SHOW_DELETED.
	liveRows, err := client.Tenant.Query().
		Where(
			enttenant.IdpOrgRefIn("org-active", "org-deleted"),
			enttenant.DeletedAtIsNil(),
		).
		Limit(mixin.Limit).
		All(sysCtx)
	require.NoError(t, err)
	require.Len(t, liveRows, 1, "DeletedAtIsNil must filter out the soft-deleted row")
	assert.Equal(t, activeID, liveRows[0].ID)
}
