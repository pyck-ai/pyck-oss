package zitadel_sync_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	zitadelsync "github.com/pyck-ai/pyck/backend/management/workflows/zitadel-sync"
)

// A sync tick that overlaps a registration must leave the new tenant alone.
// The retired Zitadel-to-DB reconcile read the Zitadel orgs and the DB
// tenants in two activities with no shared snapshot, so a tenant whose org
// was created after the first read and whose row was committed before the
// second looked like "live in DB, org gone" and was soft-deleted, and its
// delete event deactivated the brand-new org. The tick here sees both
// shapes of that window: a row still pending, and a row that has just
// reached done while no Zitadel listing ever contained its org. Neither may
// be touched, and no reconcile step may run at all.
func TestZitadelSyncWorkflow_LeavesATenantMidRegistrationAlone(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	ctx := authn.Context(t.Context(), authn.SystemUser())
	pending := seedTenantRow(t, ctx, client, "registering-tenant", enttenant.StatusPending, time.Time{})
	justDone := seedTenantRow(t, ctx, client, "just-registered-tenant", enttenant.StatusDone, time.Time{})

	env := newTestEnv()
	a := &zitadelsync.Activities{Ent: client}
	env.RegisterActivity(a.FetchDbTenantsActivity)
	rec := &startRecorder{}
	env.OnActivity(a.StartTenantSyncActivity, mock.Anything, mock.Anything).
		Run(rec.record).Return(nil)

	var mu sync.Mutex
	var scheduled []string
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		mu.Lock()
		defer mu.Unlock()
		scheduled = append(scheduled, info.ActivityType.Name)
	})

	env.ExecuteWorkflow(zitadelsync.ZitadelSyncWorkflow, zitadelsync.ZitadelSyncWorkflowInput{Period: 10 * time.Second})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	mu.Lock()
	assert.NotContains(t, scheduled, "ReconcileTenantsActivity", "a sync tick must never reconcile tenant rows against Zitadel")
	assert.NotContains(t, scheduled, "FetchZitadelTenantsActivity", "a sync tick must not take a Zitadel org snapshot to judge rows by")
	mu.Unlock()

	assert.Equal(t, []string{justDone.IdpOrgRef}, rec.tenantIDs(),
		"only the done tenant gets a user sync; the pending one has no org to sync yet")

	all := feature.Context(ctx, feature.FEATURE_SHOW_DELETED)
	for _, want := range []*ent.Tenant{pending, justDone} {
		got, err := client.Tenant.Get(all, want.ID)
		require.NoError(t, err)
		assert.True(t, got.DeletedAt.IsZero(), "tenant %q must not be soft-deleted by a sync tick", want.Name)
		assert.Equal(t, want.Status, got.Status, "tenant %q keeps its registration status", want.Name)
	}
}
