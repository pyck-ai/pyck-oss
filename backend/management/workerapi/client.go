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
	"mime"
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

// ErrNotFound is returned by StopDeployment and ResumeDeployment when
// worker-api answers that no deployment by that name exists. A 404 from an
// unknown route (an older worker-api, a wrong base path) is deliberately
// NOT mapped here: it stays an ErrRequestFailed with the status and body.
var ErrNotFound = errors.New("worker-api deployment not found")

// ErrTerminating is returned by StopDeployment and ResumeDeployment when the
// deployment is being deleted: nothing can be scaled any more.
var ErrTerminating = errors.New("worker-api deployment is being deleted")

// Temporal application-error types activities use for worker-api failures
// that retrying cannot change.
const (
	AppErrorTypeNotConfigured = "WorkerAPINotConfigured"
	// AppErrorTypeRejected: worker-api refused the request (IsPermanent).
	AppErrorTypeRejected = "WorkerAPIRejected"
)

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

// DeploymentURL is the worker-api URL of one deployment, for log lines that
// must say exactly which instance and path answered.
func (c *Client) DeploymentURL(name string) string {
	return c.baseURL + deploymentPath(name)
}

func deploymentPath(name string) string {
	return "/api/v1/deployments/" + url.PathEscape(name)
}

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
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/deployments", in, nil)
	if StatusOf(err) == http.StatusConflict {
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

	return c.doJSON(ctx, http.MethodPut, path, map[string]string{"value": value}, nil)
}

// StopDeploymentResult carries the replica count worker-api reports after
// the stop.
type StopDeploymentResult struct {
	Replicas int64 `json:"replicas"`
}

// ResumeDeploymentResult carries the replica count worker-api reports after
// the resume. ReplicasDefaulted means the deployment had no recorded size
// and was scaled to worker-api's default.
type ResumeDeploymentResult struct {
	Replicas          int64 `json:"replicas"`
	ReplicasDefaulted bool  `json:"replicas_defaulted"`
}

// StopDeployment scales a worker deployment to zero in place; the deployment
// and its secrets are kept. See scaleError for the errors callers classify.
func (c *Client) StopDeployment(ctx context.Context, name string) (StopDeploymentResult, error) {
	var out StopDeploymentResult

	err := c.doJSON(ctx, http.MethodPost, deploymentPath(name)+"/stop", nil, &out)

	return out, scaleError(err)
}

// ResumeDeployment scales a worker deployment back to the size it was
// deployed with. See scaleError for the errors callers classify.
func (c *Client) ResumeDeployment(ctx context.Context, name string) (ResumeDeploymentResult, error) {
	var out ResumeDeploymentResult

	err := c.doJSON(ctx, http.MethodPost, deploymentPath(name)+"/resume", nil, &out)

	return out, scaleError(err)
}

// scaleError translates the stop/resume statuses callers act on: a 404 is
// ErrNotFound, a 409 ErrTerminating, both only when the response carries
// worker-api's JSON error body. Without it the status came from the router,
// an ingress or a proxy, not from the handler, and is left as the request
// failure it is so it never passes for a decision about the deployment. The
// original error stays in the chain so the status and worker-api's reason
// reach workflow history.
func scaleError(err error) error {
	var se *StatusError
	if !errors.As(err, &se) || !se.jsonBody {
		return err
	}

	switch se.Status {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case http.StatusConflict:
		return fmt.Errorf("%w: %w", ErrTerminating, err)
	default:
		return err
	}
}

// StatusError is a non-2xx response. It matches ErrRequestFailed in
// errors.Is so existing callers keep working.
type StatusError struct {
	Method, Path string
	Status       int
	Body         string
	jsonBody     bool
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: %s %s: %d %s", ErrRequestFailed, e.Method, e.Path, e.Status, e.Body)
}

func (e *StatusError) Is(target error) bool { return target == ErrRequestFailed }

// HTTPStatus reports the response status, which is what the workflow error
// classifier reads to decide whether the activity may retry.
func (e *StatusError) HTTPStatus() int { return e.Status }

// StatusOf returns the HTTP status behind err, or 0 when err is not a
// StatusError.
func StatusOf(err error) int {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Status
	}

	return 0
}

// IsPermanent reports whether err is a worker-api refusal that retrying
// cannot change: a 4xx other than 404 (an ingress answers 404 while a
// route moves), 408 and 429. Callers translate 404 and 409 on stop/resume
// before consulting this.
func IsPermanent(err error) bool {
	switch s := StatusOf(err); {
	case s < 400 || s >= 500:
		return false
	case s == http.StatusNotFound, s == http.StatusRequestTimeout, s == http.StatusTooManyRequests:
		return false
	default:
		return true
	}
}

// doJSON sends body (nil for none) and decodes a 2xx response into out when
// given. Any non-2xx response is a *StatusError.
func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	if !c.Configured() {
		return ErrNotConfigured
	}

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close on a read-only request

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil {
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}

		return nil
	}

	// Cap the echoed body: it is an error message, and this lands in workflow
	// history. A body that cannot be read costs only the detail, not the error.
	detail, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		detail = []byte("(unreadable body)")
	}

	// An unparsable Content-Type is simply not JSON.
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		mediaType = ""
	}

	return &StatusError{
		Method:   method,
		Path:     path,
		Status:   resp.StatusCode,
		Body:     strings.TrimSpace(string(detail)),
		jsonBody: mediaType == "application/json",
	}
}
