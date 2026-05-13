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
