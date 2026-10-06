//go:build integration

package movementforeignids_test

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

const (
	// syncTimeout bounds the wait for zitadel-sync to project a machine user
	// into management.users.
	syncTimeout = 90 * time.Second

	// bodyExcerpt caps how much of a response body a failure message quotes.
	bodyExcerpt = 600
)

// httpClient bounds every raw request; s.Ctx carries no deadline.
var httpClient = &http.Client{Timeout: 60 * time.Second}

// tenantCtx is one provisioned tenant with its writer PAT.
type tenantCtx struct {
	RT       *gateway.RegisteredTenant
	Fixture  *fixtures.Tenant
	PAT      string
	IdpID    string // Zitadel id of the machine user
	UserID   string // management users.id of the machine user
	Username string
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

// gql posts a raw GraphQL document. Raw HTTP rather than the typed client,
// because the relation filters under test are where-input fields the
// generated operations do not carry.
func (s *MovementForeignIDSuite) gql(token, tenantID, query string, vars map[string]any) gqlResp {
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
func (s *MovementForeignIDSuite) ok(token, tenantID, query string, vars map[string]any) map[string]any {
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

// count returns the totalCount of the connection at path, or -1 when absent.
func count(v any, path ...string) int {
	n, ok := dig(v, append(path, "totalCount")...).(float64)
	if !ok {
		return -1
	}
	return int(n)
}

func uniq(prefix string) string { return prefix + "-" + uuid.NewString() }

// provision registers a fresh tenant, schedules its cleanup, mints a writer
// PAT and waits for the machine user to be projected into management.users.
func (s *MovementForeignIDSuite) provision(label string) *tenantCtx {
	s.T().Helper()
	r := s.Require()

	f := fixtures.NewTenant()
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, f)
	r.NoError(err, "register tenant %s", label)
	s.DeferTenantCleanup(rt.ID)

	p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates("writer"))
	r.NoError(err, "provision user in %s", label)

	tc := &tenantCtx{RT: rt, Fixture: f, PAT: p.PAT, IdpID: p.UserID}
	tc.UserID, tc.Username = s.waitSyncedUser(rt.ID, p.UserID)
	return tc
}

// waitSyncedUser polls (system token) until zitadel-sync projected the user
// with the given Zitadel id into the tenant, and returns its management id.
func (s *MovementForeignIDSuite) waitSyncedUser(tenantID, idpID string) (string, string) {
	s.T().Helper()
	const q = `query($t: ID!, $idp: String!) { users(first: 1, where: {tenantID: $t, idpID: $idp}) { edges { node { id username } } } }`
	var id, username string
	err := tests.PollUntil(s.Ctx, syncTimeout, 500*time.Millisecond, func() error {
		res := s.gql(s.Cfg.ServiceToken, tenantID, q, map[string]any{"t": tenantID, "idp": idpID})
		if res.failed() {
			return fmt.Errorf("users: %s", res.errText())
		}
		id = digStr(res.Data, "users", "edges", "0", "node", "id")
		username = digStr(res.Data, "users", "edges", "0", "node", "username")
		if id == "" {
			return fmt.Errorf("user %s not yet synced into %s", idpID, tenantID)
		}
		return nil
	})
	s.Require().NoError(err, "user never synced")
	return id, username
}
