//go:build integration

package tenants

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// registerTenantDocument is the mutation the raw probes below send. It is
// written out rather than taken from the generated client on purpose:
// every typed client marshals its variables from Go values, and the
// shapes under test here are JSON literals no Go value survives the trip
// as (a number float64 cannot hold, a value of the wrong kind). The
// request body is the unit under test, so the body has to be hand-built.
const registerTenantDocument = `mutation RegisterTenant($input: RegisterTenantInput!) {
  registerTenant(input: $input) { success tenant { id name status } }
}`

// postRegisterTenant sends one registerTenant request whose `data` value
// is the raw JSON literal dataLiteral, exactly as a hostile client would
// write it, and returns the decoded GraphQL response.
//
// No bearer token: registerTenant is the one anonymous mutation on the
// platform, which is the whole reason its input rules are worth probing
// at the wire.
func (s *TnRegisterSuite) postRegisterTenant(dataLiteral string) (map[string]any, int, string) {
	r := s.Require()
	t := fixtures.NewTenant()

	body := fmt.Sprintf(`{"query":%s,"variables":{"input":{
		"name":%q,"adminUsername":%q,"adminEmail":%q,
		"adminFirstName":"Test","adminLastName":"Admin",
		"adminPassword":"Str0ng!pass","data":%s}}}`,
		mustJSON(registerTenantDocument), t.Name, t.AdminUsername, t.AdminEmail, dataLiteral)

	req, err := http.NewRequestWithContext(s.Ctx, http.MethodPost,
		s.Cfg.GatewayURL, strings.NewReader(body))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer func() { r.NoError(resp.Body.Close()) }()

	raw, err := io.ReadAll(resp.Body)
	r.NoError(err)

	var decoded map[string]any
	r.NoError(json.Unmarshal(raw, &decoded), "gateway answered a non-JSON body: %s", raw)
	return decoded, resp.StatusCode, t.Name
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestRegisterTenant_RefusesDataTheRowCouldNotBeReadBackAs is the wire
// half of finding F1. gqlgen decodes every request body with
// json.Decoder.UseNumber, so a number in a GraphQL *variable* reaches the
// resolver as a json.Number holding the caller's own text, 1e400 among
// them. That is a valid JSON number and a valid GraphQL variable, and the
// tenants row stores it verbatim; every reader of the row (ent's scanner,
// the outbox payload the tenant-lifecycle trigger decodes, the
// registration run's row load, deleteTenant's own read) decodes it with a
// plain encoding/json, which fails.
//
// Accepting it therefore commits a row that holds the tenant name under
// the unique name_key index, which covers every row until a hard
// delete, while nothing can ever process or retire it: one anonymous
// request per name, permanently. The refusal has to happen
// before the insert, which is what the assertions below pin: a typed
// error, and no tenant in the response to clean up.
//
// Only a hand-written body can express this, which is why it lives here
// and not in the Bruno fixture: the fixture's YAML variables go through a
// Go decoder that cannot hold the literal either.
func (s *TnRegisterSuite) TestRegisterTenant_RefusesDataTheRowCouldNotBeReadBackAs() {
	for name, literal := range map[string]string{
		"a magnitude float64 has no room for": `{"seats":1e400}`,
		"its negative twin":                   `{"seats":-1e400}`,
		"four hundred digits":                 `{"seats":1` + strings.Repeat("0", 400) + `}`,
	} {
		s.Run(name, func() {
			r := s.Require()

			resp, status, name := s.postRegisterTenant(literal)

			// Two refusers stand in front of the row. Apollo Router parses the
			// request body itself and answers 400 for a number that does not
			// fit a float64, so most of these literals never reach management;
			// the resolver's allowlist is the second line for a client that
			// talks to the subgraph directly. Either way the refusal is typed
			// and nothing was written.
			switch status {
			case http.StatusBadRequest:
				// refused at the gateway's JSON parser
			case http.StatusOK:
				errs, ok := resp["errors"].([]any)
				r.True(ok, "expected a GraphQL error, got %v", resp)
				r.NotEmpty(errs)
				r.Contains(fmt.Sprint(errs), "tenant data",
					"the refusal must name the rule, not surface as a decode failure")
				data, _ := resp["data"].(map[string]any)
				r.Nil(data["registerTenant"], "a refused registration must return no tenant")
			default:
				r.Failf("unexpected status", "gateway answered %d: %v", status, resp)
			}

			// No tenant was created, so nothing holds the name.
			_, err := gateway.FindTenantByName(s.Ctx, s.Cfg, name)
			r.ErrorIs(err, gateway.ErrTenantNotFound, "a refused registration must leave no row holding the name")
		})
	}
}

// The same wire path with values the row can carry: a number in exponent
// form and one at the float64 ceiling are ordinary registrations, and
// what the org metadata ends up holding for them is spelled out in full
// (never "1e+06"). This is the accepted half of the same boundary: a
// refusal that also swallowed these would be a regression of its own.
func (s *TnRegisterSuite) TestRegisterTenant_AcceptsNumbersTheRowCanCarry() {
	r := s.Require()

	resp, status, _ := s.postRegisterTenant(`{"seats":1e6,"ratio":1e308,"trial":true,"flavour":"plain"}`)
	r.Equal(http.StatusOK, status)
	r.Nil(resp["errors"], "an in-range number is an ordinary registration: %v", resp["errors"])

	data, ok := resp["data"].(map[string]any)
	r.True(ok, "expected data, got %v", resp)
	registered, ok := data["registerTenant"].(map[string]any)
	r.True(ok)
	tenant, ok := registered["tenant"].(map[string]any)
	r.True(ok)

	id, _ := tenant["id"].(string)
	r.NotEmpty(id)
	s.DeferTenantCleanup(id)
	r.Equal("done", tenant["status"])
}

// A body whose `data` is not a flat map of scalars is refused at the same
// gate, before the row. Kept beside the number cases because they share
// one validator and one failure mode: a value that passes here and fails
// at SetOrgMetadataActivity fails after the Zitadel org already exists.
func (s *TnRegisterSuite) TestRegisterTenant_RefusesNonScalarData() {
	for name, literal := range map[string]string{
		"a nested object": `{"plan":{"tier":"gold"}}`,
		"an array":        `{"regions":["eu","us"]}`,
		"a null value":    `{"plan":null}`,
		"an empty string": `{"plan":""}`,
		"an empty key":    `{"":"gold"}`,
	} {
		s.Run(name, func() {
			r := s.Require()

			resp, status, _ := s.postRegisterTenant(literal)
			r.Equal(http.StatusOK, status)

			errs, ok := resp["errors"].([]any)
			r.True(ok, "expected a GraphQL error, got %v", resp)
			r.Contains(fmt.Sprint(errs), "tenant data")

			data, _ := resp["data"].(map[string]any)
			r.Nil(data["registerTenant"])
		})
	}
}

// tenantRowsLike counts every tenant row, soft-deleted ones included, whose
// name contains fragment: the vantage point the API cannot give for a name
// it refused to store.
func (s *TnRegisterSuite) tenantRowsLike(fragment string) int {
	s.T().Helper()
	out, err := tests.Psql(s.Ctx, fmt.Sprintf(
		`SELECT count(*) FROM management.tenants WHERE strpos(name, '%s') > 0`, strings.ReplaceAll(fragment, "'", "''")))
	s.Require().NoError(err)
	var n int
	_, err = fmt.Sscan(out, &n)
	s.Require().NoError(err, "count output %q", out)
	return n
}

// cleanupIfRegistered schedules the cleanup of a tenant the registration
// under test should have refused but did not, so a red run leaves nothing
// behind.
func (s *TnRegisterSuite) cleanupIfRegistered(name string) {
	s.T().Helper()
	if found, err := gateway.FindTenantByName(s.Ctx, s.Cfg, name); err == nil {
		s.DeferTenantCleanup(found.ID)
	}
}

// Zitadel trims an org name and a user's names before it stores them, and
// Postgres refuses NUL in text. registerTenant refuses, at the wire and
// before any row exists, every field with surrounding whitespace, a blank
// one, and one carrying a control character:
//
//   - " <live name>" would pass the tenant name index and collide inside
//     Zitadel, ending as a permanent error row holding the name;
//   - a whitespace-only first name would fail user import after the org
//     exists, leaving it ACTIVE;
//   - a NUL would reach the driver and come back as its own error text.
func (s *TnRegisterSuite) TestRegisterTenant_RefusesInputZitadelWouldTrimOrRefuse() {
	r := s.Require()
	live := fixtures.NewTenant()
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, live)
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)

	cases := []struct {
		name  string
		spoil func(*fixtures.Tenant)
		rule  string
	}{
		{"leading space before a live tenant's name", func(t *fixtures.Tenant) { t.Name = " " + live.Name }, "name has leading or trailing whitespace"},
		{"trailing NBSP after a live tenant's name", func(t *fixtures.Tenant) { t.Name = live.Name + "\u00a0" }, "name has leading or trailing whitespace"},
		{"whitespace-only first name", func(t *fixtures.Tenant) { t.AdminFirstName = " \u2003 " }, "adminFirstName is blank"},
		{"NUL in the name", func(t *fixtures.Tenant) { t.Name += "\x00x" }, "name contains a control character"},
	}
	for _, tc := range cases {
		s.Run(tc.name, func() {
			r := s.Require()
			probe := fixtures.NewTenant()
			tc.spoil(probe)

			got, err := gateway.RegisterTenant(s.Ctx, s.Cfg, probe)
			s.cleanupIfRegistered(probe.Name)

			r.Error(err, "%q must be refused", probe.Name)
			r.Nil(got)
			r.Contains(err.Error(), tc.rule)
			for _, leak := range []string{"pq:", "invalid byte sequence", "SQLSTATE", "marked error", "already exists"} {
				r.NotContains(err.Error(), leak, "the refusal is the input rule's, before any row or run")
			}

			// No row (and so no run and no org) for the probe: its name with
			// the spoiling stripped is held by the live tenant alone or by
			// nobody.
			stored := strings.SplitN(strings.TrimSpace(probe.Name), "\x00", 2)[0]
			want := 0
			if stored == live.Name {
				want = 1
			}
			r.Equal(want, s.tenantRowsLike(stored))
		})
	}

	r.Equal(1, s.tenantRowsLike(live.Name), "only the live tenant holds its name; no variant row was inserted")
}
