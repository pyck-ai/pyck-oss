// Package workerapi is a minimal client for the worker-api deployment control
// plane. worker-api is the only component holding write credentials on the
// worker cluster, so management reaches worker deployments through it rather
// than talking to Kubernetes directly.
package workerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrNotConfigured is returned when no worker-api URL is set.
var ErrNotConfigured = errors.New("worker-api URL is not configured")

// ErrRequestFailed wraps a non-2xx response.
var ErrRequestFailed = errors.New("worker-api request failed")

const (
	// requestTimeout stays under the caller's activity timeout so a slow
	// worker-api surfaces as a transport error, not an activity timeout.
	requestTimeout = 15 * time.Second
	// maxErrorBody caps how much of a failed response is quoted back.
	maxErrorBody = 512
)

// Client talks to worker-api as the platform service user. The token is a
// Zitadel PAT holding the SYSTEM role, which worker-api resolves by
// introspection.
type Client struct {
	baseURL string
	token   string
	httpc   *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		httpc:   &http.Client{Timeout: requestTimeout},
	}
}

// Configured reports whether the client has somewhere to send requests.
func (c *Client) Configured() bool { return c != nil && c.baseURL != "" }

// CreateDeploymentInput creates one tenant's worker deployment. Image and
// Version are omitted for shared-image extensions: those run the platform's
// image, which worker-api resolves itself.
type CreateDeploymentInput struct {
	Name              string `json:"name"`
	TenantID          string `json:"tenant_id"`
	TemporalNamespace string `json:"temporal_namespace"`
	Extension         string `json:"extension"`
	Environment       string `json:"environment"`
}

// CreateDeployment creates a worker deployment. An existing deployment is
// treated as success: tenant registration is retried by the workflow and must
// stay idempotent.
func (c *Client) CreateDeployment(ctx context.Context, in CreateDeploymentInput) error {
	err := c.do(ctx, http.MethodPost, "/api/v1/deployments", in)
	if errors.Is(err, errConflict) {
		return nil
	}

	return err
}

// SetTenantSecret stores one of a tenant's secrets in the platform secret
// store. worker-api materializes these into the worker's Kubernetes Secret and
// fails closed on a deployment whose TEMPORAL_API_KEY is missing, so this must
// precede CreateDeployment.
func (c *Client) SetTenantSecret(ctx context.Context, tenantID uuid.UUID, key, value string) error {
	path := "/api/v1/tenants/" + url.PathEscape(tenantID.String()) + "/secrets/" + url.PathEscape(key)

	return c.do(ctx, http.MethodPut, path, map[string]string{"value": value})
}

// errConflict marks a 409 so callers can treat "already exists" as success.
var errConflict = errors.New("conflict")

func (c *Client) do(ctx context.Context, method, path string, body any) error {
	if !c.Configured() {
		return ErrNotConfigured
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode %s %s: %w", method, path, err)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close on a read-only request

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusConflict {
		return errConflict
	}

	// Cap the echoed body: it is an error message, and this lands in workflow
	// history. A body that cannot be read costs only the detail, not the error.
	detail, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return fmt.Errorf("%w: %s %s: %d", ErrRequestFailed, method, path, resp.StatusCode)
	}

	return fmt.Errorf("%w: %s %s: %d %s",
		ErrRequestFailed, method, path, resp.StatusCode, strings.TrimSpace(string(detail)))
}
