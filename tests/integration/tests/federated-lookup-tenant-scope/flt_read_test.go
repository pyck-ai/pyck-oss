//go:build integration

package federatedlookuptenantscope_test

import (
	"time"

	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// The foreign reference is written through the write path, which does not
// check cross-service ids yet; once it refuses them the setup skips, because
// there is nothing stored to read through. For every reader a control proves
// the relation resolves for a same-tenant reference, so a null is evidence
// and not a broken probe.

const (
	qOrderCustomer = `query($id:ID!){pickingOrders(where:{id:$id}){edges{node{id tenantID customerID customer{id tenantID data}}}}}`
	qCustomerFiles = `query($id:ID!){customers(where:{id:$id}){edges{node{id tenantID file{id tenantID refid}}}}}`
)

// reader is one caller identity plus the acting-tenant header it sends.
type reader struct {
	name   string
	token  string
	header string
}

// readers builds the (a)/(b)/(c) callers. owner is the tenant whose row is
// read (A for the order, B for the customer); the (c) readers are omitted
// when the environment carries no system token.
func (s *FederatedLookupSuite) readers(owner, a, b *tenantCtx, mPAT string) []reader {
	both := a.RT.ID + "," + b.RT.ID
	rs := make([]reader, 0, 6)
	rs = append(rs,
		reader{"(a) single-tenant user of " + owner.Label, owner.PAT, owner.RT.ID},
		reader{"(b) multi-tenant user, header A,B", mPAT, both},
		reader{"(b) multi-tenant user, header all", mPAT, "all"},
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

// multiTenantPAT provisions a machine user in A's org, grants it the plain
// writer role (plus the per-service gate roles) in B's org too, and returns a
// fresh PAT that has been observed to act in both tenants.
func (s *FederatedLookupSuite) multiTenantPAT(a, b *tenantCtx) string {
	s.T().Helper()
	r := s.Require()
	roles := tests.RolesWithServiceGates("writer")
	m, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, a.RT, roles)
	r.NoError(err, "provision multi-tenant user in A")
	r.NoError(zitadelclient.EnsureProjectGrant(s.Ctx, s.ZConn, b.RT.IdpOrgRef, s.Cfg.ZitadelProjectID, m.UserID, roles),
		"grant multi-tenant user in B")
	// A fresh PAT so the B grant is seen by a never-introspected token.
	_, pat, err := zitadelclient.AddPAT(s.Ctx, s.ZConn, m.UserID)
	r.NoError(err)
	_, err = tests.WaitTokenReady(s.Ctx, s.Cfg, pat, b.RT.ID, 60*time.Second)
	r.NoError(err, "multi-tenant PAT never acted in B")
	_, err = tests.WaitTokenReady(s.Ctx, s.Cfg, pat, a.RT.ID, 60*time.Second)
	r.NoError(err, "multi-tenant PAT never acted in A")
	return pat
}

// TestReadPickingOrderCustomerDoesNotExposeForeignCustomer: A's order stores
// B's customer id; no reader may see B's customer through order.customer.
func (s *FederatedLookupSuite) TestReadPickingOrderCustomerDoesNotExposeForeignCustomer() {
	a, b := s.pair()
	mPAT := s.multiTenantPAT(a, b)
	aCust, bCust := s.createCustomer(a), s.createCustomer(b)

	ownOrder := digStr(s.ok(a, "createPickingOrder", mCreateOrder, map[string]any{"in": map[string]any{"customerID": aCust}}),
		"createPickingOrder", "pickingOrder", "id")
	s.Require().NotEmpty(ownOrder)
	res := s.gql(a, mCreateOrder, map[string]any{"in": map[string]any{"customerID": bCust}})
	if res.failed() {
		s.T().Skipf("write side already refuses B's customer (%s): nothing stored to read through", res.errText())
	}
	foreignOrder := digStr(res.Data, "createPickingOrder", "pickingOrder", "id")
	s.Require().NotEmpty(foreignOrder)
	s.T().Logf("A=%s B=%s A's order %s -> B's customer %s", a.RT.ID, b.RT.ID, foreignOrder, bCust)

	for _, rd := range s.readers(a, a, b, mPAT) {
		s.Run(rd.name+"/control: A's own customer resolves", func() {
			r := s.gqlAs(rd.token, rd.header, qOrderCustomer, map[string]any{"id": ownOrder})
			s.Require().Equal(ownOrder, digStr(r.Data, "pickingOrders", "edges", "0", "node", "id"),
				"reader cannot see A's order: %s", truncate(r.Body))
			s.Equal(aCust, digStr(r.Data, "pickingOrders", "edges", "0", "node", "customer", "id"), "body: %s", truncate(r.Body))
			s.Equal(a.RT.ID, digStr(r.Data, "pickingOrders", "edges", "0", "node", "customer", "tenantID"))
		})

		s.Run(rd.name+"/B's customer is not exposed through A's order", func() {
			r := s.gqlAs(rd.token, rd.header, qOrderCustomer, map[string]any{"id": foreignOrder})
			if r.failed() {
				s.T().Logf("errors: %s", r.errText())
			}
			if digStr(r.Data, "pickingOrders", "edges", "0", "node", "id") != foreignOrder {
				s.T().Skipf("reader cannot see A's order, probe proves nothing: %s", truncate(r.Body))
			}
			cust := dig(r.Data, "pickingOrders", "edges", "0", "node", "customer")
			if cust == nil {
				s.T().Logf("NOT EXPOSED: customer=null")
				return
			}
			s.T().Logf("EXPOSED: customer id=%s tenantID=%s data=%v",
				digStr(cust, "id"), digStr(cust, "tenantID"), dig(cust, "data"))
			s.Failf("B's customer exposed", "A's order (tenant %s) resolved customer %s of tenant %s",
				a.RT.ID, digStr(cust, "id"), digStr(cust, "tenantID"))
		})
	}
}

// TestReadCustomerFileDoesNotExposeForeignFile: A's file stores B's customer
// as its refid; no reader may see A's file on B's customer.
func (s *FederatedLookupSuite) TestReadCustomerFileDoesNotExposeForeignFile() {
	a, b := s.pair()
	mPAT := s.multiTenantPAT(a, b)
	bCust := s.createCustomer(b)

	bFile := digStr(s.ok(b, "createFile", mCreateFile, map[string]any{"in": map[string]any{
		"refid": bCust, "reftype": "customer", "name": uniq("csf-bfile") + ".txt", "contentType": "text/plain",
	}}), "createFile", "id")
	s.Require().NotEmpty(bFile)
	res := s.gql(a, mCreateFile, map[string]any{"in": map[string]any{
		"refid": bCust, "reftype": "customer", "name": uniq("csf-afile") + ".txt", "contentType": "text/plain",
	}})
	if res.failed() {
		s.T().Skipf("write side already refuses B's customer as refid (%s): nothing stored to read through", res.errText())
	}
	aFile := digStr(res.Data, "createFile", "id")
	s.Require().NotEmpty(aFile)
	s.T().Logf("A=%s B=%s A's file %s -> B's customer %s (B's own file %s)", a.RT.ID, b.RT.ID, aFile, bCust, bFile)

	for _, rd := range s.readers(b, a, b, mPAT) {
		s.Run(rd.name+"/control and probe: B's customer lists B's file, not A's", func() {
			r := s.gqlAs(rd.token, rd.header, qCustomerFiles, map[string]any{"id": bCust})
			if r.failed() {
				s.T().Logf("errors: %s", r.errText())
			}
			if digStr(r.Data, "customers", "edges", "0", "node", "id") != bCust {
				s.T().Skipf("reader cannot see B's customer, probe proves nothing: %s", truncate(r.Body))
			}
			files, _ := dig(r.Data, "customers", "edges", "0", "node", "file").([]any)
			var sawOwn bool
			for _, f := range files {
				switch digStr(f, "id") {
				case bFile:
					sawOwn = true
				case aFile:
					s.T().Logf("EXPOSED: file id=%s tenantID=%s on B's customer", aFile, digStr(f, "tenantID"))
					s.Failf("A's file exposed", "B's customer (tenant %s) lists A's file %s of tenant %s",
						b.RT.ID, aFile, digStr(f, "tenantID"))
				}
			}
			s.True(sawOwn, "control: B's own file %s missing from B's customer: %s", bFile, truncate(r.Body))
			if !s.T().Failed() {
				s.T().Logf("NOT EXPOSED: %d file(s), none of A's", len(files))
			}
		})
	}
}
