//go:build integration

package federatedskutenantscope_test

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
	// bodyExcerpt caps how much of a response body a failure message quotes.
	bodyExcerpt = 600

	mCreateItem  = `mutation($sku:String!){createInventoryItem(input:{sku:$sku}){inventoryItem{id sku}}}`
	mCreateRepo  = `mutation($in:CreateRepositoryInput!){createInventoryRepository(input:$in){inventoryRepository{id}}}`
	mCreateMov   = `mutation($in:CreateItemMovementInput!){createInventoryItemMovement(input:$in){inventoryItemMovement{id}}}`
	mCreateOrder = `mutation($in:CreatePickingOrderWithItemsInput!){createPickingOrder(input:$in){pickingOrder{id}}}`
)

// httpClient bounds every raw request; s.Ctx carries no deadline.
var httpClient = &http.Client{Timeout: 60 * time.Second}

// tenantCtx is one provisioned tenant with its writer PAT.
type tenantCtx struct {
	Label string
	RT    *gateway.RegisteredTenant
	PAT   string
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

// gql posts a raw GraphQL document as t, acting in t's tenant.
func (s *FederatedSkuSuite) gql(t *tenantCtx, query string, vars map[string]any) gqlResp {
	s.T().Helper()
	return s.gqlAs(t.PAT, t.RT.ID, query, vars)
}

// gqlAs posts a raw GraphQL document with an explicit bearer token and
// acting-tenant header (a single id, a comma list, or "all").
func (s *FederatedSkuSuite) gqlAs(token, tenantHeader, query string, vars map[string]any) gqlResp {
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
	req.Header.Set("X-Pyck-Tenant-Id", tenantHeader)

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
func (s *FederatedSkuSuite) ok(t *tenantCtx, what, query string, vars map[string]any) map[string]any {
	s.T().Helper()
	res := s.gql(t, query, vars)
	s.Require().False(res.failed(), "%s as %s failed: %s\nbody: %s", what, t.Label, res.errText(), truncate(res.Body))
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
// writer PAT that can reach every gated service.
func (s *FederatedSkuSuite) provision(label string) *tenantCtx {
	s.T().Helper()
	r := s.Require()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err, "register tenant %s", label)
	s.DeferTenantCleanup(rt.ID)

	p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates("writer"))
	r.NoError(err, "provision user in %s", label)

	return &tenantCtx{Label: label, RT: rt, PAT: p.PAT}
}

// pair provisions the referencing tenant A and the referenced tenant B.
func (s *FederatedSkuSuite) pair() (a, b *tenantCtx) {
	s.T().Helper()
	a, b = s.provision("A"), s.provision("B")
	s.T().Logf("A=%s B=%s", a.RT.ID, b.RT.ID)
	return a, b
}

// createItem creates an inventory item with the given SKU and returns its id.
func (s *FederatedSkuSuite) createItem(t *tenantCtx, sku string) string {
	s.T().Helper()
	id := digStr(s.ok(t, "createInventoryItem", mCreateItem, map[string]any{"sku": sku}),
		"createInventoryItem", "inventoryItem", "id")
	s.Require().NotEmpty(id)
	return id
}

// createRepo creates a root repository and returns its id.
func (s *FederatedSkuSuite) createRepo(t *tenantCtx, name string, virtual bool) string {
	s.T().Helper()
	in := map[string]any{"name": name, "type": "static", "virtualRepo": virtual}
	id := digStr(s.ok(t, "createInventoryRepository", mCreateRepo, map[string]any{"in": in}),
		"createInventoryRepository", "inventoryRepository", "id")
	s.Require().NotEmpty(id)
	return id
}

// stockUp gives t a warehouse (its only non-virtual root repository) holding
// qty units of item as incoming stock, moved in from a virtual source.
func (s *FederatedSkuSuite) stockUp(t *tenantCtx, item string, qty int) {
	s.T().Helper()
	source := s.createRepo(t, uniq("fst-source"), true)
	warehouse := s.createRepo(t, uniq("fst-warehouse"), false)
	s.ok(t, "createInventoryItemMovement", mCreateMov, map[string]any{"in": map[string]any{
		"handler": uniq("fst-h"), "fromID": source, "toID": warehouse, "itemID": item, "quantity": qty,
	}})
}

// createOrder creates a picking order in t with one item of the given SKU and
// returns the order id.
func (s *FederatedSkuSuite) createOrder(t *tenantCtx, sku string) string {
	s.T().Helper()
	in := map[string]any{"orderItems": []map[string]any{{"sku": sku, "quantity": 1}}}
	id := digStr(s.ok(t, "createPickingOrder", mCreateOrder, map[string]any{"in": in}),
		"createPickingOrder", "pickingOrder", "id")
	s.Require().NotEmpty(id)
	return id
}
