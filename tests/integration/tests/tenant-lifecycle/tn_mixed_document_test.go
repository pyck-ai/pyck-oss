//go:build integration

package tenants

import (
	"bytes"
	"encoding/json"
	"fmt"
	nethttp "net/http"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
)

// TestRegisterTenant_MixedDocumentIsRefusedAndWritesNothing proves that the
// two resource controls in front of the anonymous registration (the
// admission cap and the exemption from the request's writer transaction)
// are decided over every field the document selects, not over the first one.
//
// Both gates used to key on the first top-level field, which the caller
// writes: naming any other field before registerTenant took the mutation
// past the cap and into the writer transaction, where the resolver's
// registration wait pins a pooled connection for its full duration. Fifty
// such requests park the writer pool of a replica for every other caller.
//
// The document is sent anonymously and as raw JSON: registerTenant needs no
// token, and the generated client cannot express two root fields in one
// operation. The refusal must leave nothing behind: no tenant row under the
// submitted name, so no orphaned Zitadel organization either.
func (s *TnRegisterSuite) TestRegisterTenant_MixedDocumentIsRefusedAndWritesNothing() {
	r := s.Require()
	t := fixtures.NewTenant()

	// deleteTenant is authorized and transactional, registerTenant is
	// neither: one answer cannot serve both, so the operation is refused
	// before any resolver runs.
	document := fmt.Sprintf(`mutation {
  deleteTenant(input: {tenantId: "00000000-0000-0000-0000-000000000000"}) { success }
  %s
}`, registerTenantSelection(t))

	out := s.postAnonymousDocument(document)

	r.NotEmpty(out.Errors, "a document mixing the exempt mutation with another field must be refused")
	r.Nil(out.Data["registerTenant"], "the refused registration must not have run")

	registered, err := tenantByName(s.Ctx, s.Cfg, t.Name)
	r.Error(err, "the refusal must leave no tenant row, and with it no organization, behind")
	r.Nil(registered)
}

// TestRegisterTenant_AliasedRegistrationsAreRefusedAndWriteNothing is the
// same gate under the caller's aliases: `mutation { a: registerTenant(…)
// b: registerTenant(…) }` selects one field name twice, so a gate that
// compares the set of selected names against its list sees only exempt
// names and serves the document.
//
// Served, one anonymous request runs both registrations serially (root
// mutation fields execute in order) while holding the operation's single
// admission slot for the whole of both waits, and provisions one Zitadel
// organization per alias. With as many aliases as the cap
// (PYCK_REGISTER_TENANT_MAX_INFLIGHT), one request denies registration on
// that replica for minutes. The document must be refused, and refused
// before any registration runs: neither name may end up with a tenant row.
func (s *TnRegisterSuite) TestRegisterTenant_AliasedRegistrationsAreRefusedAndWriteNothing() {
	r := s.Require()
	first := fixtures.NewTenant()
	second := fixtures.NewTenant()

	document := fmt.Sprintf(`mutation {
  a: %s
  b: %s
}`, registerTenantSelection(first), registerTenantSelection(second))

	out := s.postAnonymousDocument(document)

	r.NotEmpty(out.Errors, "a document naming the exempt mutation twice must be refused")
	r.Nil(out.Data["a"], "the first aliased registration must not have run")
	r.Nil(out.Data["b"], "the second aliased registration must not have run")

	for _, t := range []*fixtures.Tenant{first, second} {
		registered, err := tenantByName(s.Ctx, s.Cfg, t.Name)
		r.Error(err, "the refusal must leave no tenant row for %q, and with it no organization", t.Name)
		r.Nil(registered)
	}
}

// registerTenantSelection renders one registerTenant root field for the
// given fixture. The generated client cannot express two root fields in one
// operation, so these documents are written by hand.
func registerTenantSelection(t *fixtures.Tenant) string {
	return fmt.Sprintf(
		`registerTenant(input: {name: %q, adminUsername: %q, adminEmail: %q, adminFirstName: %q, adminLastName: %q, adminPassword: %q}) { success }`,
		t.Name, t.AdminUsername, t.AdminEmail, t.AdminFirstName, t.AdminLastName, t.AdminPassword)
}

// anonymousResponse is as much of a GraphQL response as these refusal
// probes read: which root fields produced data, and whether the request was
// refused at all.
type anonymousResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// postAnonymousDocument sends a raw GraphQL document to the gateway with no
// Authorization header: registerTenant needs no token, which is what makes
// these documents deliverable by anyone.
func (s *TnRegisterSuite) postAnonymousDocument(document string) anonymousResponse {
	r := s.Require()

	body, err := json.Marshal(map[string]any{"query": document})
	r.NoError(err)

	req, err := nethttp.NewRequestWithContext(s.Ctx, nethttp.MethodPost, s.Cfg.GatewayURL, bytes.NewReader(body))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := nethttp.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close() //nolint:errcheck // response body of an assertion probe

	var out anonymousResponse
	r.NoError(json.NewDecoder(resp.Body).Decode(&out))

	return out
}
