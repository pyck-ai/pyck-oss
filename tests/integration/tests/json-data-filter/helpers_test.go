//go:build integration

package jsondatafilter_test

import (
	"bytes"
	"encoding/json"
	"errors"
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

type gqlCall struct {
	status int
	body   string
	data   map[string]json.RawMessage
	errors []string
}

func gqlString(v string) string {
	b, err := json.Marshal(v)
	if err != nil { // unreachable for a string
		panic(err)
	}
	return string(b)
}

func filterQuery(field, where string) string {
	return fmt.Sprintf(`query { %s(first: 50, where: %s) { totalCount } }`, field, where)
}

func (s *DataFilterSuite) provision() (tenantID, pat string) {
	r := s.Require()
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err, "register tenant")
	s.DeferTenantCleanup(rt.ID)
	p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates("writer"))
	r.NoError(err, "provision writer")
	return rt.ID, p.PAT
}

func (s *DataFilterSuite) post(token, tenant, query string, vars map[string]any) gqlCall {
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
	req.Header.Set("X-Pyck-Tenant-Id", tenant)
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close() //nolint:errcheck // read-only body
	body, err := io.ReadAll(resp.Body)
	r.NoError(err)
	var env struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	r.NoError(json.Unmarshal(body, &env), "decode (HTTP %d): %.300s", resp.StatusCode, body)
	call := gqlCall{status: resp.StatusCode, body: string(body), data: env.Data}
	for _, e := range env.Errors {
		call.errors = append(call.errors, e.Message)
	}
	return call
}

// totalCount reads totalCount of the connection field; a null field (the
// answer to a refused query) reads as 0.
func (s *DataFilterSuite) totalCount(c gqlCall, field string) int {
	raw, ok := c.data[field]
	if !ok || string(raw) == "null" {
		return 0
	}
	var conn struct {
		TotalCount int `json:"totalCount"`
	}
	s.Require().NoError(json.Unmarshal(raw, &conn))
	return conn.TotalCount
}

func (s *DataFilterSuite) count(field, where string) int {
	call := s.post(s.pat, s.tenant, filterQuery(field, where), nil)
	s.Require().Empty(call.errors, "query %s: %v", field, call.errors)
	return s.totalCount(call, field)
}

// createDataType registers a permissive schema for the entity. The mutation
// is admin-gated, so it runs with the system token and the tenant header.
func (s *DataFilterSuite) createDataType(tgt serviceTarget) string {
	slug := "jdf-" + strings.ReplaceAll(tgt.entity, "_", "-") + "-" + uuid.NewString()
	schema := `{"type":"object","additionalProperties":true}`
	q := fmt.Sprintf(`mutation($slug: String!, $schema: String!) { createDataType(input: {name: $slug, slug: $slug, entity: %s, jsonSchema: $schema}) { id } }`,
		gqlString(tgt.entity))
	call := s.post(s.Cfg.ServiceToken, s.tenant, q, map[string]any{"slug": slug, "schema": schema})
	s.Require().Empty(call.errors, "create %s data type", tgt.entity)
	var dt struct {
		ID string `json:"id"`
	}
	s.Require().NoError(json.Unmarshal(call.data["createDataType"], &dt))
	s.Require().NotEmpty(dt.ID)
	return dt.ID
}

// seed stores one row. The first write per service is polled: outside
// management the DataType reaches the service through a NATS-fed cache.
func (s *DataFilterSuite) seed(tgt serviceTarget, dataTypeID string, data map[string]any, await bool) {
	attempt := func() error {
		call := s.post(s.pat, s.tenant, tgt.seedMutation(dataTypeID), map[string]any{"data": data})
		if len(call.errors) > 0 {
			return errors.New(strings.Join(call.errors, " | "))
		}
		return nil
	}
	if !await {
		s.Require().NoError(attempt(), "seed %s row", tgt.service)
		return
	}
	s.Require().NoError(tests.PollUntil(s.Ctx, 30*time.Second, 500*time.Millisecond, attempt), "seed %s row", tgt.service)
}
