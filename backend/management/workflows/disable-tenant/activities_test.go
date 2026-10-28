package disabletenant_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	zitadelsdk "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	org_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/workerapi"
	disabletenant "github.com/pyck-ai/pyck/backend/management/workflows/disable-tenant"
	"github.com/pyck-ai/pyck/backend/management/workflows/internal/lifecycletest"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

func runStop(t *testing.T, client *ent.Client, api *workerapi.Client, tenantID uuid.UUID) (disabletenant.StopTenantWorkerActivityOutput, error) {
	t.Helper()

	return runStopWith(t, client, api, tenantID, true)
}

func runStopWith(t *testing.T, client *ent.Client, api *workerapi.Client, tenantID uuid.UUID, lifecycle bool) (disabletenant.StopTenantWorkerActivityOutput, error) {
	t.Helper()

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	acts := disabletenant.NewActivities(client, nil, api, commonworkflow.NewNamespaceGetter("aud"), lifecycle)
	env.RegisterActivity(acts)

	val, err := env.ExecuteActivity(acts.StopTenantWorkerActivity, disabletenant.StopTenantWorkerActivityInput{TenantID: tenantID})
	if err != nil {
		return disabletenant.StopTenantWorkerActivityOutput{}, err
	}

	var out disabletenant.StopTenantWorkerActivityOutput
	require.NoError(t, val.Get(&out))

	return out, nil
}

// disabledPyckGo seeds one soft-deleted pyck-go tenant, the disable path's
// starting point.
func disabledPyckGo(t *testing.T) *ent.Client {
	t.Helper()

	client := lifecycletest.NewEntClient(t)
	lifecycletest.CreateTenant(t, client, testTenantID, lifecycletest.PyckGoData(), true)

	return client
}

// counting answers 200 and counts the calls, for the paths that must never
// reach worker-api.
func counting(calls *int) http.HandlerFunc {
	return func(http.ResponseWriter, *http.Request) { *calls++ }
}

func TestStopTenantWorkerActivity(t *testing.T) {
	t.Parallel()

	t.Run("stops the pyck-go tenant's deployment", func(t *testing.T) {
		t.Parallel()

		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			_, _ = w.Write([]byte(`{"name":"x","status":"stopped","replicas":0}`)) //nolint:errcheck // test server
		}))
		defer srv.Close()

		out, err := runStop(t, disabledPyckGo(t), workerapi.New(srv.URL, "tok"), testTenantID)
		require.NoError(t, err)

		// The Temporal namespace IS the tenant id, so the name needs no
		// extra lookup — the same name register-tenant created.
		assert.Equal(t, "/api/v1/deployments/pyck-go-"+testTenantID.String()+"/stop", gotPath)
		assert.Empty(t, out.Skipped)
	})

	// Environments without a worker cluster switch the step off; not even
	// the DB is consulted.
	t.Run("lifecycle switched off skips without calling worker-api", func(t *testing.T) {
		t.Parallel()

		calls := 0
		srv := httptest.NewServer(counting(&calls))
		defer srv.Close()

		out, err := runStopWith(t, lifecycletest.NewEntClient(t), workerapi.New(srv.URL, "tok"), testTenantID, false)
		require.NoError(t, err)
		assert.Equal(t, registertenant.SkipLifecycleDisabled, out.Skipped)
		assert.Zero(t, calls)
	})

	t.Run("a plain tenant never calls worker-api", func(t *testing.T) {
		t.Parallel()

		client := lifecycletest.NewEntClient(t)
		lifecycletest.CreateTenant(t, client, testTenantID, map[string]any{"flavour": "pyck"}, true)

		calls := 0
		srv := httptest.NewServer(counting(&calls))
		defer srv.Close()

		out, err := runStop(t, client, workerapi.New(srv.URL, "tok"), testTenantID)
		require.NoError(t, err)
		assert.Equal(t, registertenant.SkipNotPyckGo, out.Skipped)
		assert.Zero(t, calls)
	})

	// The lifecycle trigger terminates an in-flight disable when the user
	// restores, but a stop already dispatched keeps running. It must not
	// land after the resume.
	t.Run("a tenant restored meanwhile is left alone", func(t *testing.T) {
		t.Parallel()

		client := lifecycletest.NewEntClient(t)
		lifecycletest.CreateTenant(t, client, testTenantID, lifecycletest.PyckGoData(), false)

		calls := 0
		srv := httptest.NewServer(counting(&calls))
		defer srv.Close()

		out, err := runStop(t, client, workerapi.New(srv.URL, "tok"), testTenantID)
		require.NoError(t, err)
		assert.Equal(t, disabletenant.SkipTenantRestored, out.Skipped)
		assert.Zero(t, calls)
	})

	// A pyck-go tenant whose deployment is gone runs nothing, which is the
	// goal of a stop: logged loudly, not an error.
	t.Run("missing deployment is a loud skip", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(lifecycletest.JSONStatus(http.StatusNotFound, "not found"))
		defer srv.Close()

		out, err := runStop(t, disabledPyckGo(t), workerapi.New(srv.URL, "tok"), testTenantID)
		require.NoError(t, err)
		assert.Equal(t, disabletenant.SkipNoDeployment, out.Skipped)
	})

	// A worker-api without the stop route must not pass for "no deployment":
	// that would turn every disable into a silent success. It stays
	// retryable: an ingress answers the same way while a route moves.
	t.Run("missing route is a retryable error", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()

		_, err := runStop(t, disabledPyckGo(t), workerapi.New(srv.URL, "tok"), testTenantID)
		require.ErrorContains(t, err, "404")
		lifecycletest.RequireRetryable(t, err)
	})

	t.Run("deployment being deleted is a skip", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(lifecycletest.JSONStatus(http.StatusConflict, "deployment is being deleted"))
		defer srv.Close()

		out, err := runStop(t, disabledPyckGo(t), workerapi.New(srv.URL, "tok"), testTenantID)
		require.NoError(t, err)
		assert.Equal(t, disabletenant.SkipDeleting, out.Skipped)
	})

	// Neither flow reports success while the worker is in the wrong state.
	t.Run("a stop that did not take is an error", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"name":"x","status":"stopped","replicas":1}`)) //nolint:errcheck // test server
		}))
		defer srv.Close()

		_, err := runStop(t, disabledPyckGo(t), workerapi.New(srv.URL, "tok"), testTenantID)
		require.ErrorContains(t, err, "1 replicas after stop")
	})

	t.Run("no worker cluster configured fails non-retryably", func(t *testing.T) {
		t.Parallel()

		_, err := runStop(t, disabledPyckGo(t), workerapi.New("", "tok"), testTenantID)
		lifecycletest.RequireNonRetryable(t, err, workerapi.AppErrorTypeNotConfigured)
	})

	// A refusal (bad token, platform-managed deployment) does not change
	// with time; retrying it for the whole budget would only delay the
	// failure.
	t.Run("a worker-api refusal fails non-retryably", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(lifecycletest.JSONStatus(http.StatusUnauthorized, "invalid token"))
		defer srv.Close()

		_, err := runStop(t, disabledPyckGo(t), workerapi.New(srv.URL, "tok"), testTenantID)
		lifecycletest.RequireNonRetryable(t, err, workerapi.AppErrorTypeRejected)
		require.ErrorContains(t, err, "invalid token", "worker-api's reason reaches the history")
	})

	t.Run("an outage is retryable", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(lifecycletest.JSONStatus(http.StatusServiceUnavailable, "auth backend unavailable"))
		defer srv.Close()

		_, err := runStop(t, disabledPyckGo(t), workerapi.New(srv.URL, "tok"), testTenantID)
		lifecycletest.RequireRetryable(t, err)
	})

	t.Run("unknown tenant fails non-retryably", func(t *testing.T) {
		t.Parallel()

		_, err := runStop(t, lifecycletest.NewEntClient(t), workerapi.New("http://unused", "tok"), uuid.New())
		lifecycletest.RequireNonRetryable(t, err, registertenant.AppErrorTypeTenantNotFound)
	})
}

// fakeOrgServer stands in for Zitadel's OrganizationService; reply scripts
// the gRPC status returned for each DeactivateOrganization call.
type fakeOrgServer struct {
	org_pb.UnimplementedOrganizationServiceServer

	calls atomic.Int32
	reply func(orgID string) error
}

func (f *fakeOrgServer) DeactivateOrganization(_ context.Context, req *org_pb.DeactivateOrganizationRequest) (*org_pb.DeactivateOrganizationResponse, error) {
	f.calls.Add(1)
	if err := f.reply(req.GetOrganizationId()); err != nil {
		return nil, err
	}
	return &org_pb.DeactivateOrganizationResponse{}, nil
}

// newFakeZitadel serves srv over an in-memory listener; the activities only
// need the embedded *grpc.ClientConn, so the SDK's auth plumbing is skipped.
func newFakeZitadel(t *testing.T, srv *fakeOrgServer) *zitadelsdk.Connection {
	t.Helper()

	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	org_pb.RegisterOrganizationServiceServer(server, srv)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(lis) }()
	t.Cleanup(func() {
		server.Stop()
		require.NoError(t, <-serveErr)
	})

	cc, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cc.Close()) })

	return &zitadelsdk.Connection{ClientConn: cc}
}

func grpcErr(code codes.Code, msg string) func(string) error {
	return func(string) error { return status.Error(code, msg) }
}

func testInput() disabletenant.DeactivateZitadelOrgActivityInput {
	return disabletenant.DeactivateZitadelOrgActivityInput{
		TenantID:  uuid.MustParse("4c861d67-23ea-546c-aea7-18bc43f6c62b"),
		IdpOrgRef: "329237416227374014",
	}
}

func runActivity(t *testing.T, srv *fakeOrgServer) error {
	t.Helper()

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestActivityEnvironment()
	acts := disabletenant.NewActivities(nil, newFakeZitadel(t, srv), nil, nil, false)
	env.RegisterActivity(acts)

	_, err := env.ExecuteActivity(acts.DeactivateZitadelOrgActivity, testInput())
	return err
}

func runWorkflow(t *testing.T, srv *fakeOrgServer) (*disabletenant.DisableTenantWorkflowOutput, error) {
	t.Helper()

	env := newTestEnv()
	acts := disabletenant.NewActivities(nil, newFakeZitadel(t, srv), nil, nil, false)
	env.RegisterActivity(acts)

	env.ExecuteWorkflow(disabletenant.DisableTenantWorkflow, disabletenant.DisableTenantWorkflowInput(testInput()))
	require.True(t, env.IsWorkflowCompleted())

	if err := env.GetWorkflowError(); err != nil {
		return nil, err
	}
	var out disabletenant.DisableTenantWorkflowOutput
	require.NoError(t, env.GetWorkflowResult(&out))
	return &out, nil
}

// ============================================================================
// Activity
// ============================================================================

func TestDeactivateZitadelOrgActivity_Success(t *testing.T) {
	t.Parallel()
	srv := &fakeOrgServer{reply: func(orgID string) error {
		assert.Equal(t, testInput().IdpOrgRef, orgID)
		return nil
	}}

	require.NoError(t, runActivity(t, srv))
	assert.Equal(t, int32(1), srv.calls.Load())
}

func TestDeactivateZitadelOrgActivity_AlreadyInactive_IsIdempotent(t *testing.T) {
	t.Parallel()
	srv := &fakeOrgServer{reply: grpcErr(codes.FailedPrecondition, "Organisation is already deactivated (EVENT-Dbs2g)")}

	require.NoError(t, runActivity(t, srv))
	assert.Equal(t, int32(1), srv.calls.Load())
}

func TestDeactivateZitadelOrgActivity_OrgDeleted_IsTerminalSuccess(t *testing.T) {
	t.Parallel()
	srv := &fakeOrgServer{reply: grpcErr(codes.NotFound, "Organisation not found (ORG-oL9nT)")}

	require.NoError(t, runActivity(t, srv))
	assert.Equal(t, int32(1), srv.calls.Load())
}

func TestDeactivateZitadelOrgActivity_PermanentRejection_IsNonRetryable(t *testing.T) {
	t.Parallel()

	cases := map[string]codes.Code{
		"invalid org id":    codes.InvalidArgument,
		"permission denied": codes.PermissionDenied,
	}
	for name, code := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := &fakeOrgServer{reply: grpcErr(code, "rejected by zitadel")}

			err := runActivity(t, srv)
			require.Error(t, err)

			var appErr *temporal.ApplicationError
			require.ErrorAs(t, err, &appErr)
			assert.True(t, appErr.NonRetryable())
			assert.Equal(t, "org-deactivate-rejected", appErr.Type())
			assert.Contains(t, appErr.Message(), code.String())
			assert.Contains(t, appErr.Message(), "rejected by zitadel")
		})
	}
}

func TestDeactivateZitadelOrgActivity_TransientError_IsRetryable(t *testing.T) {
	t.Parallel()
	srv := &fakeOrgServer{reply: grpcErr(codes.Unavailable, "zitadel restarting")}

	err := runActivity(t, srv)
	require.Error(t, err)

	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	assert.False(t, appErr.NonRetryable())
	assert.Contains(t, err.Error(), "zitadel restarting")
}

// ============================================================================
// Workflow: real activity through the retry policy
// ============================================================================

func TestDisableTenantWorkflow_OrgDeleted_Completes(t *testing.T) {
	t.Parallel()
	srv := &fakeOrgServer{reply: grpcErr(codes.NotFound, "Organisation not found (ORG-oL9nT)")}

	out, err := runWorkflow(t, srv)
	require.NoError(t, err)
	assert.True(t, out.Success)
	assert.Equal(t, testInput().TenantID, out.TenantID)
	assert.Equal(t, int32(1), srv.calls.Load())
}

func TestDisableTenantWorkflow_PermanentRejection_FailsWithoutRetries(t *testing.T) {
	t.Parallel()
	srv := &fakeOrgServer{reply: grpcErr(codes.PermissionDenied, "missing org.write")}

	_, err := runWorkflow(t, srv)
	require.Error(t, err)

	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, "org-deactivate-rejected", appErr.Type())
	assert.Equal(t, int32(1), srv.calls.Load())
}

func TestDisableTenantWorkflow_TransientError_ExhaustsRetryPolicy(t *testing.T) {
	t.Parallel()
	srv := &fakeOrgServer{reply: grpcErr(codes.Unavailable, "zitadel restarting")}

	_, err := runWorkflow(t, srv)
	require.Error(t, err)
	// DefaultRetryPolicy: MaximumAttempts=3.
	assert.Equal(t, int32(3), srv.calls.Load())
}

func TestDisableTenantWorkflow_TransientThenSuccess_Recovers(t *testing.T) {
	t.Parallel()
	var attempt atomic.Int32
	srv := &fakeOrgServer{reply: func(string) error {
		if attempt.Add(1) == 1 {
			return status.Error(codes.Unavailable, "zitadel restarting")
		}
		return nil
	}}

	out, err := runWorkflow(t, srv)
	require.NoError(t, err)
	assert.True(t, out.Success)
	assert.Equal(t, int32(2), srv.calls.Load())
}
