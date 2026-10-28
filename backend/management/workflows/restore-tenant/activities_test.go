package restoretenant_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/workerapi"
	"github.com/pyck-ai/pyck/backend/management/workflows/internal/lifecycletest"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
	restoretenant "github.com/pyck-ai/pyck/backend/management/workflows/restore-tenant"
)

func runResume(t *testing.T, client *ent.Client, api *workerapi.Client, tenantID uuid.UUID) (restoretenant.ResumeTenantWorkerActivityOutput, error) {
	t.Helper()

	return runResumeWith(t, client, api, tenantID, true)
}

func runResumeWith(t *testing.T, client *ent.Client, api *workerapi.Client, tenantID uuid.UUID, lifecycle bool) (restoretenant.ResumeTenantWorkerActivityOutput, error) {
	t.Helper()

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	acts := restoretenant.NewActivities(client, nil, api, commonworkflow.NewNamespaceGetter("aud"), lifecycle)
	env.RegisterActivity(acts)

	val, err := env.ExecuteActivity(acts.ResumeTenantWorkerActivity, restoretenant.ResumeTenantWorkerActivityInput{TenantID: tenantID})
	if err != nil {
		return restoretenant.ResumeTenantWorkerActivityOutput{}, err
	}

	var out restoretenant.ResumeTenantWorkerActivityOutput
	require.NoError(t, val.Get(&out))

	return out, nil
}

// activePyckGo seeds one active pyck-go tenant, the restore path's starting
// point.
func activePyckGo(t *testing.T) *ent.Client {
	t.Helper()

	client := lifecycletest.NewEntClient(t)
	lifecycletest.CreateTenant(t, client, testTenantID, lifecycletest.PyckGoData(), false)

	return client
}

func counting(calls *int) http.HandlerFunc {
	return func(http.ResponseWriter, *http.Request) { *calls++ }
}

func TestResumeTenantWorkerActivity(t *testing.T) {
	t.Parallel()

	t.Run("resumes the pyck-go tenant's deployment", func(t *testing.T) {
		t.Parallel()

		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			_, _ = w.Write([]byte(`{"name":"x","status":"resumed","replicas":2,"replicas_defaulted":false}`)) //nolint:errcheck // test server
		}))
		defer srv.Close()

		out, err := runResume(t, activePyckGo(t), workerapi.New(srv.URL, "tok"), testTenantID)
		require.NoError(t, err)
		assert.Equal(t, "/api/v1/deployments/pyck-go-"+testTenantID.String()+"/resume", gotPath)
		assert.Equal(t, restoretenant.ResumeTenantWorkerActivityOutput{Replicas: 2}, out)
	})

	t.Run("lifecycle switched off skips without calling worker-api", func(t *testing.T) {
		t.Parallel()

		calls := 0
		srv := httptest.NewServer(counting(&calls))
		defer srv.Close()

		out, err := runResumeWith(t, lifecycletest.NewEntClient(t), workerapi.New(srv.URL, "tok"), testTenantID, false)
		require.NoError(t, err)
		assert.Equal(t, registertenant.SkipLifecycleDisabled, out.Skipped)
		assert.Zero(t, calls)
	})

	t.Run("a plain tenant never calls worker-api", func(t *testing.T) {
		t.Parallel()

		client := lifecycletest.NewEntClient(t)
		lifecycletest.CreateTenant(t, client, testTenantID, map[string]any{}, false)

		calls := 0
		srv := httptest.NewServer(counting(&calls))
		defer srv.Close()

		out, err := runResume(t, client, workerapi.New(srv.URL, "tok"), testTenantID)
		require.NoError(t, err)
		assert.Equal(t, registertenant.SkipNotPyckGo, out.Skipped)
		assert.Zero(t, calls)
	})

	// The mirror of the disable-side guard: a resume dispatched before the
	// user disabled again must not land after the stop.
	t.Run("a tenant disabled meanwhile is left alone", func(t *testing.T) {
		t.Parallel()

		client := lifecycletest.NewEntClient(t)
		lifecycletest.CreateTenant(t, client, testTenantID, lifecycletest.PyckGoData(), true)

		calls := 0
		srv := httptest.NewServer(counting(&calls))
		defer srv.Close()

		out, err := runResume(t, client, workerapi.New(srv.URL, "tok"), testTenantID)
		require.NoError(t, err)
		assert.Equal(t, restoretenant.SkipTenantDisabled, out.Skipped)
		assert.Zero(t, calls)
	})

	// The tenant still ends up with a running worker; the unknown size is
	// reported, not fatal.
	t.Run("no recorded size is reported, not fatal", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"name":"x","status":"resumed","replicas":1,"replicas_defaulted":true}`)) //nolint:errcheck // test server
		}))
		defer srv.Close()

		out, err := runResume(t, activePyckGo(t), workerapi.New(srv.URL, "tok"), testTenantID)
		require.NoError(t, err)
		assert.Equal(t, restoretenant.ResumeTenantWorkerActivityOutput{Replicas: 1, ReplicasDefaulted: true}, out)
	})

	// Restore never re-provisions, so a missing deployment cannot be turned
	// into a running worker: fail loudly and non-retryably.
	t.Run("missing deployment fails non-retryably", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(lifecycletest.JSONStatus(http.StatusNotFound, "not found"))
		defer srv.Close()

		_, err := runResume(t, activePyckGo(t), workerapi.New(srv.URL, "tok"), testTenantID)
		lifecycletest.RequireNonRetryable(t, err, restoretenant.AppErrorTypeWorkerDeploymentMissing)
	})

	t.Run("deployment being deleted fails non-retryably", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(lifecycletest.JSONStatus(http.StatusConflict, "deployment is being deleted"))
		defer srv.Close()

		_, err := runResume(t, activePyckGo(t), workerapi.New(srv.URL, "tok"), testTenantID)
		lifecycletest.RequireNonRetryable(t, err, restoretenant.AppErrorTypeWorkerDeploymentDeleting)
		require.ErrorContains(t, err, "409", "worker-api's status reaches the history")
	})

	// A worker-api without the resume route is an outage, not a missing
	// deployment: it must stay retryable.
	t.Run("missing route is a retryable error", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()

		_, err := runResume(t, activePyckGo(t), workerapi.New(srv.URL, "tok"), testTenantID)
		require.ErrorContains(t, err, "404")
		lifecycletest.RequireRetryable(t, err)
	})

	t.Run("a resume that did not take is an error", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"name":"x","status":"resumed","replicas":0}`)) //nolint:errcheck // test server
		}))
		defer srv.Close()

		_, err := runResume(t, activePyckGo(t), workerapi.New(srv.URL, "tok"), testTenantID)
		require.ErrorContains(t, err, "0 replicas after resume")
	})

	t.Run("no worker cluster configured fails non-retryably", func(t *testing.T) {
		t.Parallel()

		_, err := runResume(t, activePyckGo(t), workerapi.New("", "tok"), testTenantID)
		lifecycletest.RequireNonRetryable(t, err, workerapi.AppErrorTypeNotConfigured)
	})

	t.Run("a worker-api refusal fails non-retryably", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(lifecycletest.JSONStatus(http.StatusUnprocessableEntity, "extension deployments are scaled by the platform"))
		defer srv.Close()

		_, err := runResume(t, activePyckGo(t), workerapi.New(srv.URL, "tok"), testTenantID)
		lifecycletest.RequireNonRetryable(t, err, workerapi.AppErrorTypeRejected)
	})

	t.Run("unknown tenant fails non-retryably", func(t *testing.T) {
		t.Parallel()

		_, err := runResume(t, lifecycletest.NewEntClient(t), workerapi.New("http://unused", "tok"), uuid.New())
		lifecycletest.RequireNonRetryable(t, err, registertenant.AppErrorTypeTenantNotFound)
	})
}
