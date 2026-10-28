package workerapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
		err := workerapi.New(srv.URL, "tok").CreateDeployment(t.Context(), workerapi.CreateDeploymentInput{
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
			CreateDeployment(t.Context(), workerapi.CreateDeploymentInput{Name: "x"}))
	})

	t.Run("surfaces the server's reason", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"tenant secret TEMPORAL_API_KEY is not set"}`)) //nolint:errcheck // test server
		}))
		defer srv.Close()

		err := workerapi.New(srv.URL, "tok").
			CreateDeployment(t.Context(), workerapi.CreateDeploymentInput{Name: "x"})
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
		SetTenantSecret(t.Context(), tenantID, "TEMPORAL_API_KEY", "s3cr3t"))
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
		c.CreateDeployment(t.Context(), workerapi.CreateDeploymentInput{Name: "x"}),
		workerapi.ErrNotConfigured)
}

func TestStopResumeDeployment(t *testing.T) {
	t.Parallel()

	t.Run("stop posts and decodes the verified count", func(t *testing.T) {
		t.Parallel()

		var gotPath, gotMethod, gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotMethod, gotAuth = r.URL.Path, r.Method, r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"name":"pyck-go-ns","status":"stopped","replicas":0}`)) //nolint:errcheck // test server
		}))
		defer srv.Close()

		res, err := workerapi.New(srv.URL, "tok").StopDeployment(t.Context(), "pyck-go-ns")
		require.NoError(t, err)

		assert.Equal(t, http.MethodPost, gotMethod)
		assert.Equal(t, "/api/v1/deployments/pyck-go-ns/stop", gotPath)
		assert.Equal(t, "Bearer tok", gotAuth)
		assert.Equal(t, workerapi.StopDeploymentResult{Replicas: 0}, res)
	})

	t.Run("resume posts and decodes the result", func(t *testing.T) {
		t.Parallel()

		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			_, _ = w.Write([]byte(`{"name":"pyck-go-ns","status":"resumed","replicas":1,"replicas_defaulted":true}`)) //nolint:errcheck // test server
		}))
		defer srv.Close()

		res, err := workerapi.New(srv.URL, "tok").ResumeDeployment(t.Context(), "pyck-go-ns")
		require.NoError(t, err)

		assert.Equal(t, "/api/v1/deployments/pyck-go-ns/resume", gotPath)
		assert.Equal(t, workerapi.ResumeDeploymentResult{Replicas: 1, ReplicasDefaulted: true}, res)
	})

	// worker-api's handler answers a missing deployment with its JSON error
	// body; that, and only that, is "no such deployment".
	t.Run("handler 404 is ErrNotFound", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`)) //nolint:errcheck // test server
		}))
		defer srv.Close()

		_, err := workerapi.New(srv.URL, "tok").StopDeployment(t.Context(), "ghost")
		require.ErrorIs(t, err, workerapi.ErrNotFound)
		assert.Equal(t, http.StatusNotFound, workerapi.StatusOf(err), "the status stays in the chain")

		_, err = workerapi.New(srv.URL, "tok").ResumeDeployment(t.Context(), "ghost")
		require.ErrorIs(t, err, workerapi.ErrNotFound)
	})

	// A worker-api without the stop/resume routes answers with the router's
	// plain-text 404. That must never pass for a missing deployment, or every
	// disable would silently succeed against an old worker-api.
	t.Run("route 404 stays a request failure", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()

		_, err := workerapi.New(srv.URL, "tok").StopDeployment(t.Context(), "x")
		require.NotErrorIs(t, err, workerapi.ErrNotFound)
		require.ErrorIs(t, err, workerapi.ErrRequestFailed)
		assert.Equal(t, http.StatusNotFound, workerapi.StatusOf(err))
		assert.Contains(t, err.Error(), "/api/v1/deployments/x/stop")
	})

	t.Run("409 is ErrTerminating", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"deployment is being deleted"}`)) //nolint:errcheck // test server
		}))
		defer srv.Close()

		_, err := workerapi.New(srv.URL, "tok").StopDeployment(t.Context(), "x")
		require.ErrorIs(t, err, workerapi.ErrTerminating)
		assert.Contains(t, err.Error(), "409 {\"error\":\"deployment is being deleted\"}", "worker-api's reason reaches the caller")

		_, err = workerapi.New(srv.URL, "tok").ResumeDeployment(t.Context(), "x")
		require.ErrorIs(t, err, workerapi.ErrTerminating)
	})

	// Same guard as for 404: a 409 without the handler's JSON body is not a
	// statement about the deployment.
	t.Run("non-handler 409 stays a request failure", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "conflict", http.StatusConflict)
		}))
		defer srv.Close()

		_, err := workerapi.New(srv.URL, "tok").StopDeployment(t.Context(), "x")
		require.NotErrorIs(t, err, workerapi.ErrTerminating)
		require.ErrorIs(t, err, workerapi.ErrRequestFailed)
	})

	t.Run("other failures keep status and reason", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"failed to stop deployment"}`)) //nolint:errcheck // test server
		}))
		defer srv.Close()

		_, err := workerapi.New(srv.URL, "tok").StopDeployment(t.Context(), "x")
		require.ErrorIs(t, err, workerapi.ErrRequestFailed)
		assert.Equal(t, http.StatusInternalServerError, workerapi.StatusOf(err))
		assert.Contains(t, err.Error(), "failed to stop deployment")
	})

	t.Run("not configured", func(t *testing.T) {
		t.Parallel()

		_, err := workerapi.New("", "tok").StopDeployment(t.Context(), "x")
		require.ErrorIs(t, err, workerapi.ErrNotConfigured)
		assert.Zero(t, workerapi.StatusOf(err))
	})
}

func TestActivityOptions(t *testing.T) {
	t.Parallel()

	opts := workerapi.ActivityOptions(workerapi.RetryConfig{
		StartToClose: 10 * time.Second, ScheduleToClose: 2 * time.Minute,
		InitialInterval: time.Second, MaximumInterval: 20 * time.Second,
	})
	assert.Equal(t, 10*time.Second, opts.StartToCloseTimeout)
	assert.Equal(t, 2*time.Minute, opts.ScheduleToCloseTimeout)
	assert.Equal(t, time.Second, opts.RetryPolicy.InitialInterval)
	assert.Equal(t, 20*time.Second, opts.RetryPolicy.MaximumInterval)
	assert.Zero(t, opts.RetryPolicy.MaximumAttempts, "bounded by ScheduleToClose, not by a count")
}

func TestRetryConfigValidate(t *testing.T) {
	t.Parallel()

	valid := workerapi.RetryConfig{StartToClose: 30 * time.Second, ScheduleToClose: time.Minute, InitialInterval: time.Second, MaximumInterval: time.Second}
	require.NoError(t, valid.Validate())

	zero := valid
	zero.ScheduleToClose = 0
	require.ErrorIs(t, zero.Validate(), workerapi.ErrInvalidRetryConfig)

	// An attempt shorter than the client's own request timeout would turn
	// a slow worker-api into an activity timeout instead of a transport
	// error.
	short := valid
	short.StartToClose = 5 * time.Second
	require.ErrorIs(t, short.Validate(), workerapi.ErrInvalidRetryConfig)
}

func TestIsPermanent(t *testing.T) {
	t.Parallel()

	status := func(code int) error {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		defer srv.Close()

		_, err := workerapi.New(srv.URL, "tok").ResumeDeployment(t.Context(), "x")

		return err
	}

	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity} {
		assert.True(t, workerapi.IsPermanent(status(code)), "%d", code)
	}
	for _, code := range []int{http.StatusNotFound, http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		assert.False(t, workerapi.IsPermanent(status(code)), "%d", code)
	}
	assert.False(t, workerapi.IsPermanent(workerapi.ErrNotConfigured))
}

// The registration workflow decides whether to retry a worker-api step
// from the response status: a request worker-api refuses (4xx) must end
// the step on its first attempt, a fault of its own (5xx) must be retried
// until the activity's budget is spent. A status formatted into the error
// message cannot carry that decision, so it has to be a typed field.
func TestStatusError(t *testing.T) {
	t.Parallel()

	// Every code the caller classifies differently, asserted on both
	// entry points: SetTenantSecret has no conflict fallback, so it is
	// the one that shows a 409 unswallowed.
	for _, code := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusNotFound,
		http.StatusConflict,
		http.StatusUnprocessableEntity,
		http.StatusInternalServerError,
		http.StatusServiceUnavailable,
	} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
				_, _ = w.Write([]byte(`{"error":"nope"}`)) //nolint:errcheck // test server
			}))
			defer srv.Close()

			tenantID := uuid.New()
			err := workerapi.New(srv.URL, "tok").
				SetTenantSecret(t.Context(), tenantID, "TEMPORAL_API_KEY", "s3cr3t")

			var statusErr *workerapi.StatusError
			require.ErrorAs(t, err, &statusErr, "the caller classifies on the status, not on the message")
			assert.Equal(t, code, statusErr.Status)
			assert.Equal(t, code, statusErr.HTTPStatus(), "the workflow classifier reads HTTPStatus")
			assert.Equal(t, http.MethodPut, statusErr.Method)
			assert.Equal(t, "/api/v1/tenants/"+tenantID.String()+"/secrets/TEMPORAL_API_KEY", statusErr.Path)
			assert.JSONEq(t, `{"error":"nope"}`, statusErr.Body, "the server's reason lands in workflow history")

			require.ErrorIs(t, err, workerapi.ErrRequestFailed,
				"existing callers match on the sentinel and must keep working")
			assert.Contains(t, err.Error(), fmt.Sprintf("%d", code))
			assert.Contains(t, err.Error(), "nope")
		})
	}
}

// The conflict fallback of CreateDeployment (an already created
// deployment is a successful retry, pinned by "an existing deployment is
// success" above) must not swallow any other refusal: those are what the
// registration workflow ends a step on.
func TestCreateDeployment_NonConflictRefusalsReachTheCaller(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	err := workerapi.New(srv.URL, "tok").
		CreateDeployment(t.Context(), workerapi.CreateDeploymentInput{Name: "x"})

	var statusErr *workerapi.StatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, http.StatusBadRequest, statusErr.Status,
		"only 409 is idempotent; every other refusal must reach the caller")
}

// A transport failure carries no status: it must not be reported as a
// refusal of the request, because the classifier would then end the step
// on the first attempt instead of waiting for worker-api to come back.
func TestUnreachableServerIsNotAStatusError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	err := workerapi.New(url, "tok").
		CreateDeployment(t.Context(), workerapi.CreateDeploymentInput{Name: "x"})

	require.Error(t, err)
	var statusErr *workerapi.StatusError
	require.NotErrorIs(t, err, workerapi.ErrRequestFailed, "a connection error is not a refused request")
	require.NotErrorAs(t, err, &statusErr)
}
