package workerapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/management/workerapi"
)

func TestCreateDeployment(t *testing.T) {
	t.Parallel()

	t.Run("posts the deployment and authenticates", func(t *testing.T) {
		t.Parallel()

		// The handler runs on the server's goroutine, so it only records; every
		// assertion happens on the test goroutine below.
		var gotPath, gotAuth, gotBody string
		var readErr error
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
			b, err := io.ReadAll(r.Body)
			readErr = err
			gotBody = string(b)
			w.WriteHeader(http.StatusCreated)
		}))
		defer srv.Close()

		tenantID := uuid.New()
		err := workerapi.New(srv.URL, "tok").CreateDeployment(context.Background(), workerapi.CreateDeploymentInput{
			Name: "pyckGo-ns", TenantID: tenantID.String(), TemporalNamespace: "ns",
			Extension: "pyckGo", Environment: "dev",
		})
		require.NoError(t, err)
		require.NoError(t, readErr)

		assert.Equal(t, "/api/v1/deployments", gotPath)
		assert.Equal(t, "Bearer tok", gotAuth)

		var body map[string]string
		require.NoError(t, json.Unmarshal([]byte(gotBody), &body))
		assert.Equal(t, "pyckGo", body["extension"])
		assert.Equal(t, tenantID.String(), body["tenant_id"])
		assert.NotContains(t, body, "image", "a shared-image extension takes the platform's image")
		assert.Equal(t, "dev", body["environment"],
			"worker-api serves several environments and cannot infer the tenant's")
	})

	// Tenant registration is a Temporal workflow: an activity retry must not
	// turn an already-created deployment into a permanent failure.
	t.Run("an existing deployment is success", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
		}))
		defer srv.Close()

		require.NoError(t, workerapi.New(srv.URL, "tok").
			CreateDeployment(context.Background(), workerapi.CreateDeploymentInput{Name: "x"}))
	})

	t.Run("surfaces the server's reason", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"tenant secret TEMPORAL_API_KEY is not set"}`)) //nolint:errcheck // test server
		}))
		defer srv.Close()

		err := workerapi.New(srv.URL, "tok").
			CreateDeployment(context.Background(), workerapi.CreateDeploymentInput{Name: "x"})
		require.ErrorIs(t, err, workerapi.ErrRequestFailed)
		assert.Contains(t, err.Error(), "TEMPORAL_API_KEY", "the workflow history should say why")
	})
}

func TestSetTenantSecret(t *testing.T) {
	t.Parallel()

	var gotPath, gotMethod string
	var gotBody map[string]string
	var decodeErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		decodeErr = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	tenantID := uuid.New()
	require.NoError(t, workerapi.New(srv.URL, "tok").
		SetTenantSecret(context.Background(), tenantID, "TEMPORAL_API_KEY", "s3cr3t"))
	require.NoError(t, decodeErr)

	assert.Equal(t, http.MethodPut, gotMethod)
	assert.Equal(t, "/api/v1/tenants/"+tenantID.String()+"/secrets/TEMPORAL_API_KEY", gotPath)
	assert.Equal(t, "s3cr3t", gotBody["value"])
}

// Environments with no worker cluster leave the URL empty; the caller decides
// whether that is fatal, so it must be distinguishable from a transport error.
func TestNotConfigured(t *testing.T) {
	t.Parallel()

	c := workerapi.New("", "tok")
	assert.False(t, c.Configured())
	require.ErrorIs(t,
		c.CreateDeployment(context.Background(), workerapi.CreateDeploymentInput{Name: "x"}),
		workerapi.ErrNotConfigured)
}
