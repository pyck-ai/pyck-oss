//go:build integration

package federatedskutenantscope_test

import (
	"fmt"
	"time"

	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

const (
	// systemUserID is the id the gateway gives the system user (uuid.Max).
	systemUserID = "ffffffff-ffff-ffff-ffff-ffffffffffff"

	qMe         = `query{me{UserID Tenants{ID Role}}}`
	qOrderItems = `query($id:ID!){pickingOrderItems(where:{orderID:$id}){edges{node{id tenantID sku item{id tenantID} availableStock reservedStock}}}}`

	// Stock each tenant holds of the shared SKU, as incoming stock in its
	// warehouse; different so a wrong tenant's number is visible.
	stockA = 7
	stockB = 50
)

// reader is one caller identity plus the acting-tenant header it sends.
type reader struct {
	name   string
	token  string
	header string
}

// readers builds the (a)/(b)/(c) callers for an order of tenant owner. The
// (c) readers are omitted when the environment carries no system token.
func (s *FederatedSkuSuite) readers(owner, a, b *tenantCtx, mPAT string) []reader {
	both := a.RT.ID + "," + b.RT.ID
	rs := make([]reader, 0, 6)
	rs = append(rs,
		reader{"(a) single-tenant writer of " + owner.Label, owner.PAT, owner.RT.ID},
		reader{"(b) plain reader of A and B, header A,B", mPAT, both},
		reader{"(b) plain reader of A and B, header all", mPAT, "all"},
	)
	if s.Cfg.ServiceToken == "" {
		s.T().Log("PYCK_SERVICE_TOKEN not set: (c) system-token readers skipped")
		return rs
	}
	return append(rs,
		reader{"(c) system token, header " + owner.Label, s.Cfg.ServiceToken, owner.RT.ID},
		reader{"(c) system token, header A,B", s.Cfg.ServiceToken, both},
		reader{"(c) system token, header all", s.Cfg.ServiceToken, "all"},
	)
}

// multiTenantReaderPAT provisions a machine user in A's org with the plain
// reader role (plus the per-service gate roles), grants it the same in B's
// org, and returns a fresh PAT that has been observed to act in both tenants.
func (s *FederatedSkuSuite) multiTenantReaderPAT(a, b *tenantCtx) string {
	s.T().Helper()
	r := s.Require()
	roles := tests.RolesWithServiceGates("reader")
	m, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, a.RT, roles)
	r.NoError(err, "provision multi-tenant reader in A")
	r.NoError(zitadelclient.EnsureProjectGrant(s.Ctx, s.ZConn, b.RT.IdpOrgRef, s.Cfg.ZitadelProjectID, m.UserID, roles),
		"grant multi-tenant reader in B")
	// A fresh PAT so the B grant is seen by a never-introspected token.
	_, pat, err := zitadelclient.AddPAT(s.Ctx, s.ZConn, m.UserID)
	r.NoError(err)
	_, err = tests.WaitTokenReady(s.Ctx, s.Cfg, pat, b.RT.ID, 60*time.Second)
	r.NoError(err, "multi-tenant PAT never acted in B")
	_, err = tests.WaitTokenReady(s.Ctx, s.Cfg, pat, a.RT.ID, 60*time.Second)
	r.NoError(err, "multi-tenant PAT never acted in A")

	// Prove this is an ordinary user, not the system user: its own id, and
	// the plain reader role in exactly A and B.
	me := s.gqlAs(pat, a.RT.ID+","+b.RT.ID, qMe, nil)
	r.False(me.failed(), "me as multi-tenant reader: %s", me.errText())
	r.NotEqual(systemUserID, digStr(me.Data, "me", "UserID"), "multi-tenant PAT authenticated as the system user")
	held := map[string]string{}
	for i := range 2 {
		held[digStr(me.Data, "me", "Tenants", fmt.Sprint(i), "ID")] = digStr(me.Data, "me", "Tenants", fmt.Sprint(i), "Role")
	}
	r.Equal(map[string]string{a.RT.ID: "reader", b.RT.ID: "reader"}, held, "me: %s", truncate(me.Body))
	s.T().Logf("multi-tenant reader: me.UserID=%s tenants=%v", digStr(me.Data, "me", "UserID"), held)
	return pat
}

// TestReadOrderItemResolvesOwnTenantsItemAndStock: A and B each stock the
// same SKU and each hold a picking order for it. Every reader must see each
// order item resolve to its own tenant's item and stock.
func (s *FederatedSkuSuite) TestReadOrderItemResolvesOwnTenantsItemAndStock() {
	a, b := s.pair()
	sku := uniq("fst-sku")

	// B first, so an unscoped lookup meets B's item and warehouse first.
	bItem := s.createItem(b, sku)
	s.stockUp(b, bItem, stockB)
	aItem := s.createItem(a, sku)
	s.stockUp(a, aItem, stockA)
	orders := []struct {
		owner *tenantCtx
		order string
		item  string
		stock float64
	}{
		{a, s.createOrder(a, sku), aItem, stockA},
		{b, s.createOrder(b, sku), bItem, stockB},
	}
	mPAT := s.multiTenantReaderPAT(a, b)
	s.T().Logf("A=%s B=%s sku=%s A's item %s, B's item %s", a.RT.ID, b.RT.ID, sku, aItem, bItem)

	for _, o := range orders {
		for _, rd := range s.readers(o.owner, a, b, mPAT) {
			s.Run(o.owner.Label+"'s order item/"+rd.name, func() {
				r := s.gqlAs(rd.token, rd.header, qOrderItems, map[string]any{"id": o.order})
				node := dig(r.Data, "pickingOrderItems", "edges", "0", "node")
				s.Require().NotNil(node, "reader cannot see %s's order item: %s", o.owner.Label, truncate(r.Body))
				s.Require().Equal(o.owner.RT.ID, digStr(node, "tenantID"))

				gotItem, gotTenant := digStr(node, "item", "id"), digStr(node, "item", "tenantID")
				s.T().Logf("item=%s tenant=%s availableStock=%v reservedStock=%v errors=%q",
					gotItem, gotTenant, dig(node, "availableStock"), dig(node, "reservedStock"), r.errText())
				s.Equal(o.item, gotItem, "%s's order item resolved the item of tenant %s", o.owner.Label, gotTenant)
				s.Equal(o.owner.RT.ID, gotTenant)
				got, _ := dig(node, "availableStock").(float64)
				s.InDelta(o.stock, got, 0, "%s's order item shows another tenant's stock", o.owner.Label)
			})
		}
	}
}
