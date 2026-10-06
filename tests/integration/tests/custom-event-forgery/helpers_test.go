//go:build integration

package customeventforgery_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// bodyExcerpt caps how much of a response body a failure message quotes.
const bodyExcerpt = 600

// httpClient bounds every raw request; s.Ctx carries no deadline.
var httpClient = &http.Client{Timeout: 60 * time.Second}

// tenantCtx is one provisioned tenant with its writer PAT.
type tenantCtx struct {
	RT  *gateway.RegisteredTenant
	PAT string
}

// gqlResp is one gateway round trip.
type gqlResp struct {
	Status int
	Data   map[string]any
	Errors []string
	Body   string
}

func (g gqlResp) failed() bool    { return g.Status >= 400 || len(g.Errors) > 0 }
func (g gqlResp) errText() string { return strings.Join(g.Errors, " | ") }

func truncate(s string) string {
	if len(s) <= bodyExcerpt {
		return s
	}
	return s[:bodyExcerpt] + "…"
}

// gql posts a raw GraphQL document and returns the status, data and error
// messages, so a test can assert on a refusal instead of failing on it.
func (s *CustomEventSuite) gql(token, tenantID, query string, vars map[string]any) gqlResp {
	s.T().Helper()
	r := s.Require()

	payload := map[string]any{"query": query}
	if len(vars) > 0 {
		payload["variables"] = vars
	}
	buf, err := json.Marshal(payload)
	r.NoError(err)

	req, err := http.NewRequestWithContext(s.Ctx, http.MethodPost, s.Cfg.GatewayURL, bytes.NewReader(buf))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Pyck-Tenant-Id", tenantID)

	resp, err := httpClient.Do(req)
	r.NoError(err, "gateway request")
	body, err := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	r.NoError(err)
	r.NoError(closeErr, "close gateway response")

	out := gqlResp{Status: resp.StatusCode, Body: string(body)}
	var env struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if jerr := json.Unmarshal(body, &env); jerr != nil {
		out.Errors = []string{fmt.Sprintf("non-json response (%d): %s", resp.StatusCode, truncate(string(body)))}
		return out
	}
	out.Data = env.Data
	for _, e := range env.Errors {
		out.Errors = append(out.Errors, e.Message)
	}
	if out.Status >= 400 && len(out.Errors) == 0 {
		out.Errors = []string{fmt.Sprintf("http %d: %s", out.Status, truncate(string(body)))}
	}
	return out
}

// ok runs a document that must succeed and returns its data.
func (s *CustomEventSuite) ok(token, tenantID, query string, vars map[string]any) map[string]any {
	s.T().Helper()
	res := s.gql(token, tenantID, query, vars)
	s.Require().False(res.failed(), "unexpected failure: %s\nbody: %s", res.errText(), truncate(res.Body))
	return res.Data
}

// dig walks a decoded JSON tree by object keys / array indexes ("0").
func dig(v any, path ...string) any {
	cur := v
	for _, p := range path {
		switch c := cur.(type) {
		case map[string]any:
			cur = c[p]
		case []any:
			var i int
			if _, err := fmt.Sscanf(p, "%d", &i); err != nil || i < 0 || i >= len(c) {
				return nil
			}
			cur = c[i]
		default:
			return nil
		}
	}
	return cur
}

func digStr(v any, path ...string) string {
	s, _ := dig(v, path...).(string)
	return s
}

func uniq(prefix string) string { return prefix + "-" + uuid.NewString() }

// provision registers a fresh tenant, schedules its cleanup and mints a
// writer PAT.
func (s *CustomEventSuite) provision(label string) *tenantCtx {
	s.T().Helper()
	r := s.Require()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err, "register tenant %s", label)
	s.DeferTenantCleanup(rt.ID)

	p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates("writer"))
	r.NoError(err, "provision user in %s", label)

	return &tenantCtx{RT: rt, PAT: p.PAT}
}
