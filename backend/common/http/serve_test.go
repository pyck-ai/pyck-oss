package http_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonhttp "github.com/pyck-ai/pyck/backend/common/http"
)

// serveOnLoopback starts server.Serve on a fresh loopback listener and
// returns the base URL and a channel carrying Serve's result.
func serveOnLoopback(t *testing.T, ctx context.Context, server *commonhttp.Server) (string, chan error) {
	t.Helper()

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(ctx, listener)
	}()

	return "http://" + listener.Addr().String(), serveDone
}

// httpGet performs a GET with its own context so it stays usable after the
// server's stop context is cancelled (requests must survive the drain).
func httpGet(url string) (int, string, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	body, readErr := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); readErr == nil {
		readErr = closeErr
	}
	return resp.StatusCode, string(body), readErr
}

type fakeChecker struct {
	err error
}

func (f fakeChecker) HealthCheck(context.Context) error {
	return f.err
}

type getResult struct {
	status int
	body   string
	err    error
}

// TestServeDrainsInFlightRequest is the acceptance-criteria test: a slow
// request that is in flight when the stop signal arrives completes with 200,
// the server exits within its budget, and new connections are refused after
// the drain.
func TestServeDrainsInFlightRequest(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	requestEntered := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		close(requestEntered)
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	server := commonhttp.NewServer(
		&http.Server{Handler: mux, ReadHeaderTimeout: time.Second},
		commonhttp.ServerOptions{DrainTimeout: 2 * time.Second, PreDrainDelay: 50 * time.Millisecond},
	)

	baseURL, serveDone := serveOnLoopback(t, ctx, server)

	results := make(chan getResult, 1)
	go func() {
		status, body, err := httpGet(baseURL + "/slow")
		results <- getResult{status, body, err}
	}()

	<-requestEntered
	drainStart := time.Now()
	cancel()

	select {
	case res := <-results:
		require.NoError(t, res.err, "in-flight request failed during drain")
		assert.Equal(t, http.StatusOK, res.status)
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request did not complete during drain")
	}

	select {
	case err := <-serveDone:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return within the drain budget")
	}
	assert.Less(t, time.Since(drainStart), 2*time.Second+50*time.Millisecond+time.Second,
		"drain exceeded PreDrainDelay + DrainTimeout + margin")

	_, _, err := httpGet(baseURL + "/slow")
	require.Error(t, err, "connections must be refused after the drain")
}

// TestServeDrainTimeoutBoundsStuckRequests proves the process regains
// control within its budget even when a request never finishes.
func TestServeDrainTimeoutBoundsStuckRequests(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	requestEntered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	mux := http.NewServeMux()
	mux.HandleFunc("/stuck", func(w http.ResponseWriter, r *http.Request) {
		close(requestEntered)
		<-release
	})

	server := commonhttp.NewServer(
		&http.Server{Handler: mux, ReadHeaderTimeout: time.Second},
		commonhttp.ServerOptions{DrainTimeout: 200 * time.Millisecond, PreDrainDelay: 0},
	)

	baseURL, serveDone := serveOnLoopback(t, ctx, server)

	// The response is never awaited: the request is stuck by design and only
	// completes once the deferred close(release) runs at test end.
	stuckResult := make(chan getResult, 1)
	go func() {
		status, body, err := httpGet(baseURL + "/stuck")
		stuckResult <- getResult{status, body, err}
	}()

	<-requestEntered
	cancel()

	select {
	case err := <-serveDone:
		require.Error(t, err, "an expired drain deadline must be reported")
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after the drain deadline expired")
	}
}

// TestReadinessFlipsToDrainingDuringPreDrainWindow: readiness reports 503
// as soon as shutdown begins — while the pre-drain window still serves
// traffic — and the request context of an in-flight request is not
// cancelled by the drain.
func TestReadinessFlipsToDrainingDuringPreDrainWindow(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	requestEntered := make(chan struct{})
	requestCtxErr := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		close(requestEntered)
		time.Sleep(300 * time.Millisecond)
		requestCtxErr <- r.Context().Err()
		w.WriteHeader(http.StatusOK)
	})

	server := commonhttp.NewServer(
		&http.Server{Handler: mux, ReadHeaderTimeout: time.Second},
		commonhttp.ServerOptions{DrainTimeout: 2 * time.Second, PreDrainDelay: time.Second},
	)
	// Registered after NewServer, mirroring the services' wiring: the mux
	// stays mutable until the first request.
	mux.Handle("/health/ready", server.ReadinessHandler(fakeChecker{}))

	baseURL, serveDone := serveOnLoopback(t, ctx, server)

	status, _, err := httpGet(baseURL + "/health/ready")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status, "ready before shutdown")

	results := make(chan getResult, 1)
	go func() {
		s, b, e := httpGet(baseURL + "/slow")
		results <- getResult{s, b, e}
	}()
	<-requestEntered

	cancel()

	// The pre-drain window (1s) is still serving: readiness must already
	// report draining while new requests are still accepted.
	require.Eventually(t, func() bool {
		status, _, err := httpGet(baseURL + "/health/ready")
		return err == nil && status == http.StatusServiceUnavailable
	}, 500*time.Millisecond, 20*time.Millisecond, "readiness must flip to 503 during the pre-drain window")

	select {
	case ctxErr := <-requestCtxErr:
		require.NoError(t, ctxErr, "in-flight request context must not be cancelled by the drain")
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request never finished")
	}

	select {
	case res := <-results:
		require.NoError(t, res.err)
		assert.Equal(t, http.StatusOK, res.status)
	case <-time.After(time.Second):
		t.Fatal("in-flight response never arrived")
	}

	select {
	case err := <-serveDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}

// TestReadinessHandlerReportsComponentFailure: without draining, the handler
// delegates to the component checks.
func TestReadinessHandlerReportsComponentFailure(t *testing.T) {
	t.Parallel()

	server := commonhttp.NewServer(
		&http.Server{ReadHeaderTimeout: time.Second},
		commonhttp.ServerOptions{},
	)

	rec := httptest.NewRecorder()
	server.ReadinessHandler(fakeChecker{err: errors.New("db down")}).
		ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health/ready", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	rec = httptest.NewRecorder()
	server.ReadinessHandler(fakeChecker{}).
		ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health/ready", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// blockingChecker blocks until its context is cancelled, simulating a
// wedged (as opposed to down) dependency.
type blockingChecker struct{}

func (blockingChecker) HealthCheck(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestLivenessHandlerReturnsOK: liveness answers unconditionally, with the
// same body/content-type shape as readiness.
func TestLivenessHandlerReturnsOK(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	commonhttp.LivenessHandler().
		ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health/live", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
}

// TestLivenessHandlerIgnoresDraining is the important property: liveness
// must stay 200 while a server drains, or an orchestrator would restart a
// pod that is deliberately finishing its in-flight requests.
func TestLivenessHandlerIgnoresDraining(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux := http.NewServeMux()
	mux.Handle("/health/live", commonhttp.LivenessHandler())

	server := commonhttp.NewServer(
		&http.Server{Handler: mux, ReadHeaderTimeout: time.Second},
		commonhttp.ServerOptions{DrainTimeout: 2 * time.Second, PreDrainDelay: time.Second},
	)

	baseURL, serveDone := serveOnLoopback(t, ctx, server)

	cancel()

	require.Eventually(t, func() bool {
		status, _, err := httpGet(baseURL + "/health/live")
		return err == nil && status == http.StatusOK
	}, 500*time.Millisecond, 20*time.Millisecond, "liveness must stay 200 during the pre-drain window")

	select {
	case err := <-serveDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}

// TestReadinessHandlerTimesOutOnBlockedComponent: a component that blocks
// past readinessCheckTimeout must produce a 503, not hang the handler.
//
// The request context carries its own short deadline rather than waiting
// out the real readinessCheckTimeout: context.WithTimeout bounds the child
// context by whichever deadline is sooner, so the handler's internal 2s
// timeout never gets a chance to be the limiting factor here.
func TestReadinessHandlerTimesOutOnBlockedComponent(t *testing.T) {
	t.Parallel()

	server := commonhttp.NewServer(
		&http.Server{ReadHeaderTimeout: time.Second},
		commonhttp.ServerOptions{},
	)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/health/ready", nil)

	done := make(chan struct{})
	go func() {
		server.ReadinessHandler(blockingChecker{}).ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-done:
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	case <-time.After(2 * time.Second):
		t.Fatal("readiness handler did not return after the component check timed out")
	}
}

// TestServeDrainsOnRealSIGTERM proves the signal wiring end to end: a real
// SIGTERM delivered to the process cancels the NotifyContext-derived context
// and drains the server.
//
//nolint:paralleltest // the signal is process-wide; other NotifyContext users in this binary would observe it
func TestServeDrainsOnRealSIGTERM(t *testing.T) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	requestEntered := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		close(requestEntered)
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	server := commonhttp.NewServer(
		&http.Server{Handler: mux, ReadHeaderTimeout: time.Second},
		commonhttp.ServerOptions{DrainTimeout: 2 * time.Second, PreDrainDelay: 50 * time.Millisecond},
	)

	baseURL, serveDone := serveOnLoopback(t, ctx, server)

	results := make(chan getResult, 1)
	go func() {
		status, body, err := httpGet(baseURL + "/slow")
		results <- getResult{status, body, err}
	}()

	<-requestEntered
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGTERM))

	select {
	case res := <-results:
		require.NoError(t, res.err, "in-flight request must survive SIGTERM")
		assert.Equal(t, http.StatusOK, res.status)
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request did not complete after SIGTERM")
	}

	select {
	case err := <-serveDone:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after SIGTERM")
	}
}
