package tenantreconcile_test

import (
	"context"
	"net"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	orgv2_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	tenantreconcile "github.com/pyck-ai/pyck/backend/management/workflows/tenant-reconcile"
)

// fakeOrgServer answers ListOrganizations with a fixed org list so the
// activity's Zitadel side can be driven without a live instance.
type fakeOrgServer struct {
	orgv2_pb.UnimplementedOrganizationServiceServer

	orgs []*orgv2_pb.Organization
	// onList, when set, runs inside the ListOrganizations handler, i.e.
	// between the activity's disabled-rows snapshot and its restore-side
	// lookup. It is the seam for injecting a concurrent DB write into that
	// window, which no static seed can reach.
	onList func()
}

func (s *fakeOrgServer) ListOrganizations(_ context.Context, _ *orgv2_pb.ListOrganizationsRequest) (*orgv2_pb.ListOrganizationsResponse, error) {
	if s.onList != nil {
		s.onList()
	}
	return &orgv2_pb.ListOrganizationsResponse{Result: s.orgs}, nil
}

// newZitadelConn serves fake over bufconn and returns the Connection the
// activity dials the org v2 service on.
func newZitadelConn(t *testing.T, fake *fakeOrgServer) *zitadel.Connection {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	orgv2_pb.RegisterOrganizationServiceServer(srv, fake)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.GracefulStop()
		if err := <-serveErr; err != nil {
			t.Errorf("bufconn server: %v", err)
		}
	})

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err, "dial bufconn")
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Logf("close bufconn client: %v", err)
		}
	})

	return &zitadel.Connection{ClientConn: conn}
}

// seedDisabledTenant inserts a soft-deleted tenant row in the given
// registration status, referencing orgID.
func seedDisabledTenant(t *testing.T, ctx context.Context, client *ent.Client, name, orgID string, status enttenant.Status) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := client.Tenant.Create().
		SetID(id).
		SetName(name).
		SetIdpOrgRef(orgID).
		SetStatus(status).
		SetDeletedAt(time.Now().UTC()).
		Save(ctx)
	require.NoError(t, err)
	return id
}

// A pending registration that an operator retired with deleteTenant
// leaves a soft-deleted status=pending row; its delete event dispatched
// DisableTenantWorkflow, so an org still ACTIVE is one created after that
// (the run was mid-flight) and belongs to the operator. That tombstone
// never was a live tenant, so it is not disable drift: the reconciler
// must not dispatch DisableTenantWorkflow against it. Only the fully
// registered (status=done) tombstone with an ACTIVE org is drift.
func TestComputeDriftActivity_IgnoresPendingTombstones(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	sysCtx := authn.Context(t.Context(), authn.SystemUser())
	doneID := seedDisabledTenant(t, sysCtx, client, "done-tenant", "org-done", enttenant.StatusDone)
	seedDisabledTenant(t, sysCtx, client, "pending-tenant", "org-pending", enttenant.StatusPending)

	conn := newZitadelConn(t, &fakeOrgServer{orgs: []*orgv2_pb.Organization{
		{Id: "org-done", State: orgv2_pb.OrganizationState_ORGANIZATION_STATE_ACTIVE},
		{Id: "org-pending", State: orgv2_pb.OrganizationState_ORGANIZATION_STATE_ACTIVE},
	}})
	a := tenantreconcile.NewActivities(client, nil, conn, tenantreconcile.Config{})

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.ComputeDriftActivity)

	val, err := env.ExecuteActivity(a.ComputeDriftActivity, tenantreconcile.ComputeDriftActivityInput{})
	require.NoError(t, err)

	var drift tenantreconcile.DriftSet
	require.NoError(t, val.Get(&drift))

	require.Equal(t, []tenantreconcile.TenantRef{{TenantID: doneID, IdpOrgRef: "org-done"}}, drift.ToDisable,
		"only the status=done tombstone with an ACTIVE org is disable drift")
	assert.Empty(t, drift.ToRestore, "both rows are soft-deleted, nothing to restore")
}

// A failed registration (status=error) that an operator retired leaves a
// soft-deleted row whose org (if the run got that far) is still ACTIVE.
// It never was a live tenant either: the reconciler must leave the org to
// deleteTenant's own DisableTenantWorkflow and not treat it as drift.
func TestComputeDriftActivity_IgnoresErrorTombstones(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	sysCtx := authn.Context(t.Context(), authn.SystemUser())
	doneID := seedDisabledTenant(t, sysCtx, client, "done-tenant", "org-done", enttenant.StatusDone)
	seedDisabledTenant(t, sysCtx, client, "error-tenant", "org-error", enttenant.StatusError)

	conn := newZitadelConn(t, &fakeOrgServer{orgs: []*orgv2_pb.Organization{
		{Id: "org-done", State: orgv2_pb.OrganizationState_ORGANIZATION_STATE_ACTIVE},
		{Id: "org-error", State: orgv2_pb.OrganizationState_ORGANIZATION_STATE_ACTIVE},
	}})
	a := tenantreconcile.NewActivities(client, nil, conn, tenantreconcile.Config{})

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.ComputeDriftActivity)

	val, err := env.ExecuteActivity(a.ComputeDriftActivity, tenantreconcile.ComputeDriftActivityInput{})
	require.NoError(t, err)

	var drift tenantreconcile.DriftSet
	require.NoError(t, val.Get(&drift))

	require.Equal(t, []tenantreconcile.TenantRef{{TenantID: doneID, IdpOrgRef: "org-done"}}, drift.ToDisable,
		"the error tombstone's org is not disable drift")
	assert.Empty(t, drift.ToRestore)
}

// seedLiveTenant inserts a live (not soft-deleted) tenant row in the given
// registration status, referencing orgID.
func seedLiveTenant(t *testing.T, ctx context.Context, client *ent.Client, name, orgID string, status enttenant.Status) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := client.Tenant.Create().
		SetID(id).
		SetName(name).
		SetIdpOrgRef(orgID).
		SetStatus(status).
		Save(ctx)
	require.NoError(t, err)
	return id
}

// A live pending or error row is a registration that never finished. Its
// org can be INACTIVE while the row is live (an operator deactivating a
// half-built org during cleanup) and that is not restore drift: without a
// status predicate on the restore-side lookup the sweep would re-activate
// the org every cycle, fighting the operator. Only the live status=done
// row with an INACTIVE org is restore drift.
func TestComputeDriftActivity_LiveUnfinishedRowsAreNotRestoreDrift(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	sysCtx := authn.Context(t.Context(), authn.SystemUser())
	doneID := seedLiveTenant(t, sysCtx, client, "done-tenant", "org-done", enttenant.StatusDone)
	seedLiveTenant(t, sysCtx, client, "error-tenant", "org-error", enttenant.StatusError)
	seedLiveTenant(t, sysCtx, client, "pending-tenant", "org-pending", enttenant.StatusPending)

	conn := newZitadelConn(t, &fakeOrgServer{orgs: []*orgv2_pb.Organization{
		{Id: "org-done", State: orgv2_pb.OrganizationState_ORGANIZATION_STATE_INACTIVE},
		{Id: "org-error", State: orgv2_pb.OrganizationState_ORGANIZATION_STATE_INACTIVE},
		{Id: "org-pending", State: orgv2_pb.OrganizationState_ORGANIZATION_STATE_INACTIVE},
	}})
	a := tenantreconcile.NewActivities(client, nil, conn, tenantreconcile.Config{})

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.ComputeDriftActivity)

	val, err := env.ExecuteActivity(a.ComputeDriftActivity, tenantreconcile.ComputeDriftActivityInput{})
	require.NoError(t, err)

	var drift tenantreconcile.DriftSet
	require.NoError(t, val.Get(&drift))

	assert.Empty(t, drift.ToDisable)
	require.Equal(t, []tenantreconcile.TenantRef{{TenantID: doneID, IdpOrgRef: "org-done"}}, drift.ToRestore,
		"a live done row with an INACTIVE org is restore drift; live pending/error rows are not")
}

// A soft-delete that lands between the disabled snapshot and the
// restore-side lookup is the one sequence a static seed cannot produce: at
// the snapshot the row is live (so its org is not in the disabled set), and
// by the lookup it is tombstoned. The lookup runs under
// FEATURE_SHOW_DELETED, so without its DeletedAtIsNil predicate the
// freshly-deleted row leaks into ToRestore and the sweep overrides the
// admin's most-recent intent. The fake Zitadel handler runs exactly in that
// window, so the delete is injected there.
func TestComputeDriftActivity_ConcurrentSoftDeleteDoesNotRestore(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	sysCtx := authn.Context(t.Context(), authn.SystemUser())
	victimID := seedLiveTenant(t, sysCtx, client, "victim", "org-victim", enttenant.StatusDone)

	fake := &fakeOrgServer{orgs: []*orgv2_pb.Organization{
		{Id: "org-victim", State: orgv2_pb.OrganizationState_ORGANIZATION_STATE_INACTIVE},
	}}
	fake.onList = func() {
		require.NoError(t, client.Tenant.Update().
			Where(enttenant.IDEQ(victimID)).
			SetDeletedAt(time.Now().UTC()).
			Exec(sysCtx))
	}

	conn := newZitadelConn(t, fake)
	a := tenantreconcile.NewActivities(client, nil, conn, tenantreconcile.Config{})

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.ComputeDriftActivity)

	val, err := env.ExecuteActivity(a.ComputeDriftActivity, tenantreconcile.ComputeDriftActivityInput{})
	require.NoError(t, err)

	var drift tenantreconcile.DriftSet
	require.NoError(t, val.Get(&drift))
	assert.Empty(t, drift.ToRestore,
		"a row soft-deleted mid-sweep must not be restored against the admin's intent")
	assert.Empty(t, drift.ToDisable)
}
