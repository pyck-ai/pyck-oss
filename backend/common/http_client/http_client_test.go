package httpclient_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	httpclient "github.com/pyck-ai/pyck/backend/common/http_client"
	"github.com/pyck-ai/pyck/backend/common/requestid"
)

func TestMain(m *testing.M) {
	// Match the production global propagator wired in backend/common/otel
	// so otelhttp.Transport injects baggage into outbound headers.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	m.Run()
}

func TestMakeRequestPropagatesBaggageOutbound(t *testing.T) {
	t.Parallel()

	const id = "01010101-0202-7303-8404-050505050505"

	receivedBaggage := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBaggage <- r.Header.Get("Baggage")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	ctx, err := requestid.WithRequestID(context.Background(), id)
	require.NoError(t, err)

	body, err := httpclient.MakeRequest(ctx, http.MethodGet, server.URL, nil, nil, nil, true)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(body))

	got := <-receivedBaggage
	require.NotEmpty(t, got, "outbound request must carry the W3C baggage header")
	assert.Contains(t, got, "pyck.request-id="+id, "outbound baggage header must contain the request-id member")
}

func TestMakeRequestSendsHeadersBodyAndQueryParams(t *testing.T) {
	t.Parallel()

	type captured struct {
		method string
		path   string
		query  string
		header string
		body   string
	}
	captureCh := make(chan captured, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captureCh <- captured{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.Query().Get("q"),
			header: r.Header.Get("X-Custom"),
			body:   string(body),
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	headers := map[string]string{"X-Custom": "abc"}
	queryParams := map[string]string{"q": "hello"}
	body := []byte(`{"k":"v"}`)

	_, err := httpclient.MakeRequest(context.Background(), http.MethodPost, server.URL+"/path", headers, queryParams, body, true)
	require.NoError(t, err)

	got := <-captureCh
	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/path", got.path)
	assert.Equal(t, "hello", got.query)
	assert.Equal(t, "abc", got.header)
	assert.JSONEq(t, `{"k":"v"}`, got.body)
}

func TestMakeRequestWrapsUnexpectedStatus(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	body, err := httpclient.MakeRequest(context.Background(), http.MethodGet, server.URL, nil, nil, nil, true)
	require.Error(t, err)
	assert.Nil(t, body)
	require.ErrorIs(t, err, httpclient.ErrUnexpectedStatus, "error must wrap ErrUnexpectedStatus so callers can errors.Is on it")
	assert.Contains(t, err.Error(), "500")
}

func TestMakeRequestRespectsContextCancellation(t *testing.T) {
	t.Parallel()

	// Server hangs until the client disconnects.
	releaseServer := make(chan struct{})
	defer close(releaseServer)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-releaseServer:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := httpclient.MakeRequest(ctx, http.MethodGet, server.URL, nil, nil, nil, true)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, time.Second, "context deadline must abort the in-flight request")
}

func TestMakeRequestInvalidURLReturnsError(t *testing.T) {
	t.Parallel()

	_, err := httpclient.MakeRequest(context.Background(), http.MethodGet, "://not-a-url", nil, nil, nil, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse", "url parse failure must be reported")
}
