package registertenant_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	commonwf "github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/management/workerapi"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

// The worker's personal access token is a live credential with a
// multi-year life. An activity that returned it would write it into
// workflow history, where it stays readable for the history's retention
// and is replayed on every worker restart, which is why minting and
// storing are one activity. These tests pin that invariant from both
// sides: the credential does reach the secret store, and it appears in no
// activity input and no activity output.

// workerAPIRecorder is a worker-api that answers 204 and keeps every
// request body, so a test can see what actually travelled to the secret
// store.
type workerAPIRecorder struct {
	srv    *httptest.Server
	bodies []string
	// paths carries the request targets: the secret's key and the tenant
	// travel in the URL, the value in the body.
	paths []string
}

func newWorkerAPIRecorder(t *testing.T) *workerAPIRecorder {
	t.Helper()
	r := &workerAPIRecorder{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err == nil {
			r.bodies = append(r.bodies, string(body))
		}
		r.paths = append(r.paths, req.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// activitiesWithRecorder wires the Zitadel fake and the recording
// worker-api into one Activities value, which is the seam pair the merged
// identity step uses.
func activitiesWithRecorder(t *testing.T, f *fakeZitadel, rec *workerAPIRecorder) *registertenant.Activities {
	t.Helper()
	a := registertenant.NewActivities(nil, nil, nil,
		commonwf.NewNamespaceGetter("test-audience"), workerapi.New(rec.srv.URL, "tok"))
	f.install(a)
	return a
}

// The credential must land in the secret store (worker-api fails closed on
// a deployment whose TEMPORAL_API_KEY is missing) and must not be
// anywhere in what Temporal would persist for the step.
func TestProvisionTenantWorkerIdentityActivity_TokenReachesTheStoreAndNotTheHistory(t *testing.T) {
	t.Parallel()

	const secret = "pat-value-that-must-never-be-persisted"
	f := newFakeZitadel()
	f.users.patToken = secret
	rec := newWorkerAPIRecorder(t)
	a := activitiesWithRecorder(t, f, rec)

	input := registertenant.ProvisionTenantWorkerIdentityActivityInput{
		OrganizationID: "org-1",
		TenantID:       uuid.New(),
		Key:            "TEMPORAL_API_KEY",
	}
	out, err := a.ProvisionTenantWorkerIdentityActivity(t.Context(), input)
	require.NoError(t, err)

	require.Len(t, rec.bodies, 1, "the credential is pushed exactly once")
	assert.Contains(t, rec.bodies[0], secret, "the worker cannot start without the credential in the store")
	require.Len(t, rec.paths, 1)
	assert.Contains(t, rec.paths[0], "TEMPORAL_API_KEY", "it is stored under the key the deployment reads")
	assert.Contains(t, rec.paths[0], input.TenantID.String(), "and under the tenant that owns the worker")

	// Temporal persists the JSON of an activity's input and output; that is
	// the exact text this assertion searches.
	inJSON, err := json.Marshal(any(input))
	require.NoError(t, err)
	// Marshalled through any: what is asserted is the bytes Temporal would
	// persist, not a tagged-struct contract.
	outJSON, err := json.Marshal(any(out))
	require.NoError(t, err)

	assert.NotContains(t, string(inJSON), secret, "the credential must not be an activity input")
	assert.NotContains(t, string(outJSON), secret, "the credential must not be an activity output")
	assert.Equal(t, registertenant.WorkerUserID("org-1"), out.UserID, "only the user ID comes back")
}

// A failed push must not leak the credential through the error either: the
// run's error travels into history and, for a registration, all the way to
// the resolver's caller.
func TestProvisionTenantWorkerIdentityActivity_FailedPushDoesNotLeakTheToken(t *testing.T) {
	t.Parallel()

	const secret = "pat-value-that-must-never-be-persisted"
	f := newFakeZitadel()
	f.users.patToken = secret
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"secret rejected"}`)) //nolint:errcheck // test server
	}))
	t.Cleanup(srv.Close)

	a := registertenant.NewActivities(nil, nil, nil,
		commonwf.NewNamespaceGetter("test-audience"), workerapi.New(srv.URL, "tok"))
	f.install(a)

	out, err := a.ProvisionTenantWorkerIdentityActivity(t.Context(),
		registertenant.ProvisionTenantWorkerIdentityActivityInput{
			OrganizationID: "org-1", TenantID: uuid.New(), Key: "TEMPORAL_API_KEY",
		})

	require.Error(t, err)
	assert.Nil(t, out)
	assert.NotContains(t, err.Error(), secret, "a failing push must not put the credential in the error")
}

// The invariant is structural, not incidental: neither the input nor the
// output type may grow a field that carries a secret. A field added back
// would be serialized into history by the next run without anyone noticing.
func TestProvisionTenantWorkerIdentity_TypesCarryNoCredentialField(t *testing.T) {
	t.Parallel()

	forbidden := []string{"token", "secret", "password", "credential", "value", "hash"}
	for name, typ := range map[string]reflect.Type{
		"input":  reflect.TypeOf(registertenant.ProvisionTenantWorkerIdentityActivityInput{}),
		"output": reflect.TypeOf(registertenant.ProvisionTenantWorkerIdentityOutput{}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for i := range typ.NumField() {
				field := strings.ToLower(typ.Field(i).Name)
				for _, word := range forbidden {
					assert.NotContains(t, field, word,
						"%s.%s would carry the worker credential into workflow history",
						typ.Name(), typ.Field(i).Name)
				}
			}
		})
	}
}

// End to end through the workflow: a full pyck-go run must record no
// credential in any activity input, which is what the merge bought. The
// admin password hash is held to the same rule by the run's other half.
func TestRegisterTenantWorkflow_NoActivityInputCarriesACredential(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	m := newActivityMock()
	m.loadReturn.Data = pyckGoData()
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertCalled(t, "ProvisionTenantWorkerIdentityActivity", mock.Anything, mock.Anything)

	require.NotEmpty(t, m.recorded, "the run must have recorded its activity inputs")
	for _, recorded := range m.recorded {
		assert.NotContains(t, recorded, "PasswordHash", "the admin hash is read from the row inside its activity")
		assert.NotContains(t, recorded, `"Token"`, "no activity input carries a token field")
		assert.NotContains(t, recorded, `"Value"`, "the secret-store push happens inside the identity activity")
	}
}
