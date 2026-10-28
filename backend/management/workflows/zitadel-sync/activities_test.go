package zitadel_sync_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	zitadelsync "github.com/pyck-ai/pyck/backend/management/workflows/zitadel-sync"
)

// seedTenant inserts a tenant row in the given registration status; a
// non-zero deletedAt soft-deletes it.
func seedTenantRow(t *testing.T, ctx context.Context, client *ent.Client, name string, status enttenant.Status, deletedAt time.Time) *ent.Tenant {
	t.Helper()
	id := uuid.New()
	q := client.Tenant.Create().
		SetID(id).
		SetName(name).
		SetIdpOrgRef("org-" + id.String()).
		SetStatus(status)
	if !deletedAt.IsZero() {
		q = q.SetDeletedAt(deletedAt)
	}
	row, err := q.Save(ctx)
	require.NoError(t, err)
	return row
}

// A pending tenant has no Zitadel org yet and a soft-deleted one has no
// live users to project; only done, live rows are fanned out to
// TenantSyncWorkflow.
func TestFetchDbTenantsActivity_ExcludesPendingAndDeletedTenants(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	ctx := authn.Context(t.Context(), authn.SystemUser())
	done := seedTenantRow(t, ctx, client, "done-tenant", enttenant.StatusDone, time.Time{})
	seedTenantRow(t, ctx, client, "pending-tenant", enttenant.StatusPending, time.Time{})
	seedTenantRow(t, ctx, client, "deleted-done-tenant", enttenant.StatusDone, time.Now().UTC())

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	a := &zitadelsync.Activities{Ent: client}
	env.RegisterActivity(a.FetchDbTenantsActivity)

	val, err := env.ExecuteActivity(a.FetchDbTenantsActivity, zitadelsync.FetchDbTenantsInput{})
	require.NoError(t, err)

	var got []string
	require.NoError(t, val.Get(&got))

	require.Len(t, got, 1, "only the active, fully registered tenant takes part in sync")
	assert.Equal(t, done.IdpOrgRef, got[0], "sync keys tenants by their Zitadel org")
}

// A registration that failed (status=error) may have an org, a half-made
// org or none at all; syncing users for it would either wipe or invent a
// projection. Only done rows are fanned out.
func TestFetchDbTenantsActivity_ExcludesErrorTenants(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	ctx := authn.Context(t.Context(), authn.SystemUser())
	done := seedTenantRow(t, ctx, client, "done-tenant", enttenant.StatusDone, time.Time{})
	seedTenantRow(t, ctx, client, "error-tenant", enttenant.StatusError, time.Time{})

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	a := &zitadelsync.Activities{Ent: client}
	env.RegisterActivity(a.FetchDbTenantsActivity)

	val, err := env.ExecuteActivity(a.FetchDbTenantsActivity, zitadelsync.FetchDbTenantsInput{})
	require.NoError(t, err)

	var got []string
	require.NoError(t, val.Get(&got))

	require.Equal(t, []string{done.IdpOrgRef}, got, "an error row must not be synced")
}

// Every live, done tenant is fanned out (not just the first page or the
// first match) and the listing is keyed by idp_org_ref, never by the
// tenant UUID or name.
func TestFetchDbTenantsActivity_ReturnsEveryLiveDoneOrgRef(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	ctx := authn.Context(t.Context(), authn.SystemUser())
	want := make([]string, 0, 3)
	for _, name := range []string{"alpha", "beta", "gamma"} {
		want = append(want, seedTenantRow(t, ctx, client, name, enttenant.StatusDone, time.Time{}).IdpOrgRef)
	}

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	a := &zitadelsync.Activities{Ent: client}
	env.RegisterActivity(a.FetchDbTenantsActivity)

	val, err := env.ExecuteActivity(a.FetchDbTenantsActivity, zitadelsync.FetchDbTenantsInput{})
	require.NoError(t, err)

	var got []string
	require.NoError(t, val.Get(&got))
	assert.ElementsMatch(t, want, got)
}

// With no live, done tenant the listing is empty rather than an error, so
// the orchestrator ends an idle cycle cleanly instead of failing its tick.
func TestFetchDbTenantsActivity_EmptyWhenNoLiveDoneTenant(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	ctx := authn.Context(t.Context(), authn.SystemUser())
	seedTenantRow(t, ctx, client, "pending-tenant", enttenant.StatusPending, time.Time{})
	seedTenantRow(t, ctx, client, "deleted-done-tenant", enttenant.StatusDone, time.Now().UTC())

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	a := &zitadelsync.Activities{Ent: client}
	env.RegisterActivity(a.FetchDbTenantsActivity)

	val, err := env.ExecuteActivity(a.FetchDbTenantsActivity, zitadelsync.FetchDbTenantsInput{})
	require.NoError(t, err)

	var got []string
	require.NoError(t, val.Get(&got))
	assert.Empty(t, got)
}

// The listing's scope is its own predicate, not the privacy policy: a
// context carrying FEATURE_SHOW_DELETED makes HistoryMixinQueryFilter skip,
// and a soft-deleted tenant must still not be fanned out to user sync.
func TestFetchDbTenantsActivity_ExcludesDeletedEvenWhenShowDeletedIsSet(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	ctx := authn.Context(t.Context(), authn.SystemUser())
	live := seedTenantRow(t, ctx, client, "live-tenant", enttenant.StatusDone, time.Time{})
	seedTenantRow(t, ctx, client, "deleted-done-tenant", enttenant.StatusDone, time.Now().UTC())

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.SetWorkerOptions(worker.Options{
		BackgroundActivityContext: feature.Context(ctx, feature.FEATURE_SHOW_DELETED),
	})
	a := &zitadelsync.Activities{Ent: client}
	env.RegisterActivity(a.FetchDbTenantsActivity)

	val, err := env.ExecuteActivity(a.FetchDbTenantsActivity, zitadelsync.FetchDbTenantsInput{})
	require.NoError(t, err)

	var got []string
	require.NoError(t, val.Get(&got))

	require.Len(t, got, 1, "a soft-deleted tenant is out of scope even when the context shows deleted rows")
	assert.Equal(t, live.IdpOrgRef, got[0])
}

// LimitMixin silently caps an unpaginated query at mixin.Limit rows; the
// listing pages through the table so tenants past that cap are still synced.
func TestFetchDbTenantsActivity_ListsBeyondTheDefaultQueryLimit(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	ctx := authn.Context(t.Context(), authn.SystemUser())
	want := make([]string, 0, mixin.Limit+1)
	for i := range mixin.Limit + 1 {
		want = append(want, seedTenantRow(t, ctx, client, fmt.Sprintf("tenant-%d", i), enttenant.StatusDone, time.Time{}).IdpOrgRef)
	}

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	a := &zitadelsync.Activities{Ent: client}
	env.RegisterActivity(a.FetchDbTenantsActivity)

	val, err := env.ExecuteActivity(a.FetchDbTenantsActivity, zitadelsync.FetchDbTenantsInput{})
	require.NoError(t, err)

	var got []string
	require.NoError(t, val.Get(&got))

	require.Len(t, got, mixin.Limit+1, "every live, done tenant is listed, not only the first default-limit page")
	assert.ElementsMatch(t, want, got)
}
