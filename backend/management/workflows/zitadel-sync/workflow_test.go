package zitadel_sync_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	zitadelsync "github.com/pyck-ai/pyck/backend/management/workflows/zitadel-sync"
)

// newTestEnv relaxes the deadlock detector (1s of wall-clock per workflow
// task by default), which a loaded CI runner exceeds under -race. Neither
// workflow here blocks outside Temporal timers, so the timeout only removes
// the flake.
func newTestEnv() *testsuite.TestWorkflowEnvironment {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: time.Minute})
	return env
}

// startRecorder captures every StartTenantSyncActivity input in the order the
// orchestrator scheduled it. The test environment runs each mocked activity
// on its own goroutine and the orchestrator fires several starts per tick
// without waiting for them, so record calls overlap; mu guards inputs.
type startRecorder struct {
	mu     sync.Mutex
	inputs []zitadelsync.StartTenantSyncActivityInput
}

func (r *startRecorder) record(args mock.Arguments) {
	in, ok := args.Get(1).(zitadelsync.StartTenantSyncActivityInput)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs = append(r.inputs, in)
}

func (r *startRecorder) snapshot() []zitadelsync.StartTenantSyncActivityInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]zitadelsync.StartTenantSyncActivityInput(nil), r.inputs...)
}

func (r *startRecorder) tenantIDs() []string {
	inputs := r.snapshot()
	ids := make([]string, len(inputs))
	for i, in := range inputs {
		ids[i] = in.TenantID
	}
	return ids
}

// The orchestrator's whole job after the cut: one TenantSyncWorkflow start
// per org the DB lists, addressed by org ID on the sync task queue, and
// nothing else scheduled (an unmocked activity fails the test environment).
func TestZitadelSyncWorkflow_StartsOneSyncPerLiveTenant(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	var a zitadelsync.Activities
	rec := &startRecorder{}
	env.OnActivity(a.FetchDbTenantsActivity, mock.Anything, mock.Anything).
		Return([]string{"org-c", "org-a", "org-b"}, nil)
	env.OnActivity(a.StartTenantSyncActivity, mock.Anything, mock.Anything).
		Run(rec.record).Return(nil)

	env.ExecuteWorkflow(zitadelsync.ZitadelSyncWorkflow, zitadelsync.ZitadelSyncWorkflowInput{Period: 10 * time.Second})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertNumberOfCalls(t, "FetchDbTenantsActivity", 1)
	env.AssertNumberOfCalls(t, "StartTenantSyncActivity", 3)
	assert.ElementsMatch(t, []string{"org-a", "org-b", "org-c"}, rec.tenantIDs(),
		"every listed org gets exactly one sync start, keyed by its Zitadel org ID")
	for _, in := range rec.snapshot() {
		assert.Equal(t, zitadelsync.TenantSyncTaskQueue, in.TaskQueue)
		assert.Equal(t, zitadelsync.TenantWorkflowIDPrefix, in.WorkflowIDPrefix)
	}
}

// A single-tenant DB (every fresh dev and feature stack) must still get
// its one start: the empty-listing guard is the only early exit, and an
// off-by-one there would silently stop user sync everywhere with one
// tenant.
func TestZitadelSyncWorkflow_SingleTenantIsStarted(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	var a zitadelsync.Activities
	rec := &startRecorder{}
	env.OnActivity(a.FetchDbTenantsActivity, mock.Anything, mock.Anything).
		Return([]string{"org-a"}, nil)
	env.OnActivity(a.StartTenantSyncActivity, mock.Anything, mock.Anything).
		Run(rec.record).Return(nil)

	env.ExecuteWorkflow(zitadelsync.ZitadelSyncWorkflow, zitadelsync.ZitadelSyncWorkflowInput{Period: 10 * time.Second})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertNumberOfCalls(t, "StartTenantSyncActivity", 1)
	assert.Equal(t, []string{"org-a"}, rec.tenantIDs(), "the only tenant is started exactly once")
}

// Fairness across windows: the sorted org list is rotated by the window
// ordinal so the same tenant is not always synced first. With Period=10s
// and n=3 the ordinal of a window starting at 10s·k is k, so k ≡ 1 (mod 3)
// must emit sorted[1], sorted[2], sorted[0] in that order.
func TestZitadelSyncWorkflow_RotatesStartOrderByWindow(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	env.SetStartTime(time.Unix(70, 0).UTC()) // 10s·7 → ordinal 7 → start index 1

	var a zitadelsync.Activities
	rec := &startRecorder{}
	env.OnActivity(a.FetchDbTenantsActivity, mock.Anything, mock.Anything).
		Return([]string{"org-c", "org-a", "org-b"}, nil)
	env.OnActivity(a.StartTenantSyncActivity, mock.Anything, mock.Anything).
		Run(rec.record).Return(nil)

	env.ExecuteWorkflow(zitadelsync.ZitadelSyncWorkflow, zitadelsync.ZitadelSyncWorkflowInput{Period: 10 * time.Second})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	assert.Equal(t, []string{"org-b", "org-c", "org-a"}, rec.tenantIDs(),
		"starts are emitted in sorted order rotated by the window ordinal")
}

// An empty DB listing ends the cycle without scheduling anything; it must
// not error, or the schedule would report every idle environment as failing.
func TestZitadelSyncWorkflow_NoTenantsNoFanOut(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	var a zitadelsync.Activities
	env.OnActivity(a.FetchDbTenantsActivity, mock.Anything, mock.Anything).
		Return([]string{}, nil)
	env.OnActivity(a.StartTenantSyncActivity, mock.Anything, mock.Anything).
		Return(nil)

	env.ExecuteWorkflow(zitadelsync.ZitadelSyncWorkflow, zitadelsync.ZitadelSyncWorkflowInput{Period: 10 * time.Second})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertNotCalled(t, "StartTenantSyncActivity", mock.Anything, mock.Anything)
}

// If the DB listing fails the cycle fails visibly and fans out to nobody;
// starting syncs from a partial or absent list would silently skip tenants.
func TestZitadelSyncWorkflow_FetchFailureSurfacesWithoutFanOut(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	var a zitadelsync.Activities
	env.OnActivity(a.FetchDbTenantsActivity, mock.Anything, mock.Anything).
		Return(nil, temporal.NewNonRetryableApplicationError("db down", "DBError", nil))
	env.OnActivity(a.StartTenantSyncActivity, mock.Anything, mock.Anything).
		Return(nil)

	env.ExecuteWorkflow(zitadelsync.ZitadelSyncWorkflow, zitadelsync.ZitadelSyncWorkflowInput{Period: 10 * time.Second})

	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	require.Error(t, err)
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, "DBError", appErr.Type())
	env.AssertNotCalled(t, "StartTenantSyncActivity", mock.Anything, mock.Anything)
}

// One tenant's start failing (its retry budget exhausted) must not abort
// the cycle: the remaining tenants are still started and the orchestrator
// completes, leaving the failed tenant to the next tick.
func TestZitadelSyncWorkflow_StartFailureDoesNotAbortOtherTenants(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	var a zitadelsync.Activities
	rec := &startRecorder{}
	env.OnActivity(a.FetchDbTenantsActivity, mock.Anything, mock.Anything).
		Return([]string{"org-a", "org-b", "org-c"}, nil)
	env.OnActivity(a.StartTenantSyncActivity, mock.Anything, mock.MatchedBy(func(in zitadelsync.StartTenantSyncActivityInput) bool {
		return in.TenantID == "org-b"
	})).Run(rec.record).Return(temporal.NewNonRetryableApplicationError("temporal unavailable", "StartError", nil))
	env.OnActivity(a.StartTenantSyncActivity, mock.Anything, mock.Anything).
		Run(rec.record).Return(nil)

	env.ExecuteWorkflow(zitadelsync.ZitadelSyncWorkflow, zitadelsync.ZitadelSyncWorkflowInput{Period: 10 * time.Second})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "a single failed start is logged, not propagated")
	assert.ElementsMatch(t, []string{"org-a", "org-b", "org-c"}, rec.tenantIDs(),
		"every tenant is still attempted")
}

// legacyTenant is the wire shape the retired tenant listings returned; the
// gate decodes only ID from it.
type legacyTenant struct {
	ID      string
	Name    string
	Flavour string
}

// An execution started before the one-way sync carries no "one-way-sync"
// marker, so GetVersion answers DefaultVersion and the orchestrator must
// re-issue the retired FetchZitadelTenants → FetchDb → ReconcileTenants →
// FetchDb sequence and fan out from the second DB listing; any other
// command sequence is a NonDeterministicError on replay. The retired
// activities are registered here under their names only so the
// environment can serve them; in production their results come from
// history.
func TestZitadelSyncWorkflow_ReplaysPreGateListingSequence(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	env.OnGetVersion("one-way-sync", workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)

	env.RegisterActivityWithOptions(func(context.Context) ([]legacyTenant, error) { return nil, nil },
		activity.RegisterOptions{Name: "FetchZitadelTenantsActivity"})
	env.RegisterActivityWithOptions(func(context.Context) ([]legacyTenant, error) { return nil, nil },
		activity.RegisterOptions{Name: "FetchDbTenantsActivity"})
	env.RegisterActivityWithOptions(func(context.Context) error { return nil },
		activity.RegisterOptions{Name: "ReconcileTenantsActivity"})

	var a zitadelsync.Activities
	rec := &startRecorder{}
	env.OnActivity("FetchZitadelTenantsActivity", mock.Anything).
		Return([]legacyTenant{{ID: "org-z", Name: "zitadel-only"}}, nil)
	env.OnActivity("FetchDbTenantsActivity", mock.Anything).
		Return([]legacyTenant{{ID: "org-b", Name: "b", Flavour: "pyckGo"}, {ID: "org-a", Name: "a"}}, nil)
	env.OnActivity("ReconcileTenantsActivity", mock.Anything).Return(nil)
	env.OnActivity(a.StartTenantSyncActivity, mock.Anything, mock.Anything).
		Run(rec.record).Return(nil)

	env.ExecuteWorkflow(zitadelsync.ZitadelSyncWorkflow, zitadelsync.ZitadelSyncWorkflowInput{Period: 10 * time.Second})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertNumberOfCalls(t, "FetchZitadelTenantsActivity", 1)
	env.AssertNumberOfCalls(t, "FetchDbTenantsActivity", 2)
	env.AssertNumberOfCalls(t, "ReconcileTenantsActivity", 1)
	assert.ElementsMatch(t, []string{"org-a", "org-b"}, rec.tenantIDs(),
		"fan-out follows the DB listing, not the Zitadel one")
}

var testUsers = []zitadelsync.User{
	{ID: "u1", Username: "alice", Email: "alice@example.com", TenantID: "org-a", IsOwner: true},
	{ID: "u2", Username: "bob", Email: "bob@example.com", TenantID: "org-a"},
}

// Happy path of the per-tenant projection: both listings feed
// ReconcileUsersActivity unchanged, scoped to the tenant's org ID.
func TestTenantSyncWorkflow_ReconcilesFetchedUsers(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	var a zitadelsync.Activities
	dbUsers := testUsers[:1]
	env.OnActivity(a.FetchZitadelUsersActivity, mock.Anything, zitadelsync.FetchZitadelUsersActivityInput{TenantID: "org-a"}).
		Return(testUsers, nil)
	env.OnActivity(a.FetchDbUsersActivity, mock.Anything, zitadelsync.FetchDbUsersActivityInput{TenantID: "org-a"}).
		Return(dbUsers, nil)
	env.OnActivity(a.ReconcileUsersActivity, mock.Anything, zitadelsync.ReconcileUsersActivityInput{
		ZitadelUsers: testUsers,
		DbUsers:      dbUsers,
		TenantID:     "org-a",
	}).Return(nil)

	env.ExecuteWorkflow(zitadelsync.TenantSyncWorkflow, zitadelsync.TenantSyncWorkflowInput{TenantID: "org-a"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertNumberOfCalls(t, "ReconcileUsersActivity", 1)
}

// An org deleted out of band is the ErrZitadelOrgNotFound skip path: the run
// completes without touching the DB, so the tenant's last projected users
// survive and the run does not burn retries. Reconciling against an empty
// Zitadel listing here would soft-delete every user of a live tenant.
func TestTenantSyncWorkflow_SkipsUserSyncWhenOrgIsGone(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	var a zitadelsync.Activities
	env.OnActivity(a.FetchZitadelUsersActivity, mock.Anything, mock.Anything).
		Return(nil, temporal.NewNonRetryableApplicationError("org not found", zitadelsync.ErrZitadelOrgNotFound, nil))
	env.OnActivity(a.FetchDbUsersActivity, mock.Anything, mock.Anything).
		Return([]zitadelsync.User{}, nil)
	env.OnActivity(a.ReconcileUsersActivity, mock.Anything, mock.Anything).
		Return(nil)

	env.ExecuteWorkflow(zitadelsync.TenantSyncWorkflow, zitadelsync.TenantSyncWorkflowInput{TenantID: "org-a"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "a missing org completes the run instead of failing it")
	env.AssertNotCalled(t, "FetchDbUsersActivity", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "ReconcileUsersActivity", mock.Anything, mock.Anything)
}

// Only the NotFound classification is skipped; any other Zitadel failure
// fails the run so the schedule surfaces it, and the DB is left untouched.
func TestTenantSyncWorkflow_SurfacesOtherZitadelFailures(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	var a zitadelsync.Activities
	env.OnActivity(a.FetchZitadelUsersActivity, mock.Anything, mock.Anything).
		Return(nil, temporal.NewNonRetryableApplicationError("permission denied", "ZitadelPermissionDenied", nil))
	env.OnActivity(a.FetchDbUsersActivity, mock.Anything, mock.Anything).
		Return([]zitadelsync.User{}, nil)
	env.OnActivity(a.ReconcileUsersActivity, mock.Anything, mock.Anything).
		Return(nil)

	env.ExecuteWorkflow(zitadelsync.TenantSyncWorkflow, zitadelsync.TenantSyncWorkflowInput{TenantID: "org-a"})

	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	require.Error(t, err)
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, "ZitadelPermissionDenied", appErr.Type())
	env.AssertNotCalled(t, "ReconcileUsersActivity", mock.Anything, mock.Anything)
}

// A failed DB listing must not be reconciled as "no users": that would
// recreate every user as new and orphan the existing rows.
func TestTenantSyncWorkflow_DbFetchFailureSkipsReconcile(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	var a zitadelsync.Activities
	env.OnActivity(a.FetchZitadelUsersActivity, mock.Anything, mock.Anything).
		Return(testUsers, nil)
	env.OnActivity(a.FetchDbUsersActivity, mock.Anything, mock.Anything).
		Return(nil, temporal.NewNonRetryableApplicationError("db down", "DBError", nil))
	env.OnActivity(a.ReconcileUsersActivity, mock.Anything, mock.Anything).
		Return(nil)

	env.ExecuteWorkflow(zitadelsync.TenantSyncWorkflow, zitadelsync.TenantSyncWorkflowInput{TenantID: "org-a"})

	require.True(t, env.IsWorkflowCompleted())
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, env.GetWorkflowError(), &appErr)
	assert.Equal(t, "DBError", appErr.Type())
	env.AssertNotCalled(t, "ReconcileUsersActivity", mock.Anything, mock.Anything)
}
