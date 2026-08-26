package httpclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// ErrUnexpectedStatus is returned by MakeRequest when the response status
// falls outside the 2xx range. Wrap-friendly via errors.Is.
var ErrUnexpectedStatus = errors.New("unexpected response status")

// secureClient is the default HTTP client used by MakeRequest. Its transport
// is wrapped with otelhttp so OTel trace context and baggage (including
// pyck.request-id) propagate automatically to outbound calls.
var secureClient = &http.Client{
	Transport: otelhttp.NewTransport(http.DefaultTransport),
}

// NewInstrumentedClient returns a dedicated HTTP client for long-lived
// service-to-service request/response use (e.g. the management API client):
// otelhttp-wrapped transport so trace context and baggage propagate to the
// remote host, and its own connection pool decoupled from the mutable global
// http.DefaultClient.
//
// timeout caps each entire attempt — dial, headers, and body — so a
// black-holed endpoint cannot pin calls whose contexts carry no deadline
// (request contexts deliberately don't). Because of that total cap, this
// constructor is unsuitable for streaming or large-download clients.
//
// The transport is a clone of http.DefaultTransport and inherits its
// defaults (30s dial, 10s TLS handshake, 90s IdleConnTimeout); only the
// per-host idle pool is set from maxIdleConnsPerHost. That pool is a reuse
// pool, not a concurrency cap: bursts beyond it still run, on short-lived
// extra connections. Values <= 0 keep the stdlib per-host default (2),
// which redials constantly under concurrent traffic to a single host —
// callers normally pass config.GatewayConfig's tuned value.
//
// The caller owns the pool: release idle connections on shutdown via
// Client.CloseIdleConnections.
func NewInstrumentedClient(timeout time.Duration, maxIdleConnsPerHost int) *http.Client {
	var transport *http.Transport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	} else {
		// in case http.DefaultTransport was replaced with a custom RoundTripper,
		// create an empty one
		transport = &http.Transport{}
	}
	if maxIdleConnsPerHost > 0 {
		transport.MaxIdleConnsPerHost = maxIdleConnsPerHost
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: otelhttp.NewTransport(transport),
	}
}

// insecureClient mirrors secureClient but with TLS verification disabled.
// Reserved for trusted internal endpoints with self-signed certificates;
// never use against untrusted hosts.
//
//nolint:gosec // G402: InsecureSkipVerify is intentional for self-signed dev/internal endpoints
var insecureClient = &http.Client{
	Transport: otelhttp.NewTransport(&http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}),
}

// MakeRequest issues an HTTP request and returns the response body. The given
// context governs cancellation and supplies the OTel baggage / trace context
// that otelhttp propagates to the remote host.
func MakeRequest(ctx context.Context, method string, baseUrl string, headers map[string]string, queryParams map[string]string, body []byte, secure bool) ([]byte, error) {
	requestUrl, err := url.Parse(baseUrl)
	if err != nil {
		return nil, fmt.Errorf("failed to parse url: %w", err)
	}

	if queryParams != nil {
		query := requestUrl.Query()
		for key, value := range queryParams {
			query.Set(key, value)
		}
		requestUrl.RawQuery = query.Encode()
	}

	request, err := http.NewRequestWithContext(ctx, method, requestUrl.String(), bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	for key, value := range headers {
		request.Header.Set(key, value)
	}

	requestClient := secureClient
	if !secure {
		requestClient = insecureClient
	}

	response, err := requestClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: %d", ErrUnexpectedStatus, response.StatusCode)
	}

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	return responseBody, nil
}
