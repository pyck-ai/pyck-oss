//go:build integration

package relationfilterisolation_test

import (
	"fmt"
	"time"

	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// TestGuestCheckInStaysScopedToTheActingTenant covers the legitimate flow that
// makes a row of A reference a row of B: B's machine user, granted writer in
// A, checks in on one of A's devices. management.device_users.user_id then
// points at a users row whose tenant_id is B. A's own users must not be able
// to evaluate that row, or anything reachable from it, through the deviceUsers
// relation filter — while the same filter keeps matching A's own users.
func (s *RelationFilterSuite) TestGuestCheckInStaysScopedToTheActingTenant() {
	r := s.Require()
	a := s.provision("A")
	b := s.provision("B")

	// B's machine user becomes a guest writer of A.
	r.NoError(zitadelclient.EnsureProjectGrant(s.Ctx, s.ZConn, a.RT.IdpOrgRef, s.Cfg.ZitadelProjectID, b.IdpID,
		tests.RolesWithServiceGates("writer")), "grant B's user writer in A")
	_, guestPAT, err := zitadelclient.AddPAT(s.Ctx, s.ZConn, b.IdpID)
	r.NoError(err)
	_, err = tests.WaitTokenReady(s.Ctx, s.Cfg, guestPAT, a.RT.ID, 60*time.Second)
	r.NoError(err, "guest token never accepted for A")

	// Two A devices at an A location (check-in requires one): one for the
	// guest, one for A's own user (the positive control).
	loc := s.createLocation(a, uniq("rf-loc"))
	guestDev := s.createDevice(a, uniq("rf-guest-dev"), loc)
	ownDev := s.createDevice(a, uniq("rf-own-dev"), loc)

	ci := s.ok(guestPAT, a.RT.ID, `mutation($d: UUID!) { checkInUserDevice(input: {deviceID: $d}) { deviceUser { id userID } } }`,
		map[string]any{"d": guestDev})
	guestDU := digStr(ci, "checkInUserDevice", "deviceUser", "id")
	r.NotEmpty(guestDU)
	r.Equal(b.UserID, digStr(ci, "checkInUserDevice", "deviceUser", "userID"),
		"the guest's device_users row must reference the B-homed users row")

	own := s.ok(a.PAT, a.RT.ID, `mutation($d: UUID!) { checkInUserDevice(input: {deviceID: $d}) { deviceUser { id } } }`,
		map[string]any{"d": ownDev})
	ownDU := digStr(own, "checkInUserDevice", "deviceUser", "id")
	r.NotEmpty(ownDU)

	// Control: the direct read is scoped, so the relation filter must be too.
	s.Run("control: A cannot read B's users row directly", func() {
		res := s.gql(a.PAT, a.RT.ID, fmt.Sprintf(`{ users(where: {username: %q}) { totalCount } }`, b.Username), nil)
		s.Require().False(res.failed(), res.errText())
		s.Equal(0, count(res.Data, "users"))
	})

	s.Run("positive control: hasUserWith still matches A's own user", func() {
		res := s.gql(a.PAT, a.RT.ID, fmt.Sprintf(`{ deviceUsers(where: {hasUserWith: {username: %q}}) { totalCount edges { node { id } } } }`, a.Username), nil)
		s.Require().False(res.failed(), res.errText())
		s.Contains(connIDs(res.Data, "deviceUsers"), ownDU, "the relation filter must keep working inside the acting tenant")
	})

	s.Run("hasUserWith does not evaluate the B-homed users row", func() {
		for _, where := range []string{
			fmt.Sprintf(`{hasUserWith: {username: %q}}`, b.Username),
			fmt.Sprintf(`{hasUserWith: {idpID: %q}}`, b.IdpID),
			fmt.Sprintf(`{hasUserWith: {tenantID: %q}}`, b.RT.ID),
			fmt.Sprintf(`{hasUserWith: {usernameHasPrefix: %q}}`, b.Username[:3]),
		} {
			res := s.gql(a.PAT, a.RT.ID, fmt.Sprintf(`{ deviceUsers(where: %s) { totalCount edges { node { id } } } }`, where), nil)
			s.Require().False(res.failed(), res.errText())
			s.NotContains(connIDs(res.Data, "deviceUsers"), guestDU,
				"deviceUsers(where: %s) matched through the B-homed users row", where)
		}
	})

	s.Run("chained relation filters do not reach B's tenant or B's other users", func() {
		admin := b.Fixture.AdminUsername
		s.Require().NoError(tests.PollUntil(s.Ctx, syncTimeout, 500*time.Millisecond, func() error {
			res := s.gql(s.Cfg.ServiceToken, b.RT.ID, fmt.Sprintf(`{ users(where: {tenantID: %q, username: %q}) { totalCount } }`, b.RT.ID, admin), nil)
			if count(res.Data, "users") != 1 {
				return fmt.Errorf("B admin %s not synced yet (%s)", admin, res.errText())
			}
			return nil
		}), "B's admin never synced")

		for _, where := range []string{
			fmt.Sprintf(`{hasUserWith: {hasTenantWith: {name: %q}}}`, b.RT.Name),
			fmt.Sprintf(`{hasUserWith: {hasTenantWith: {hasTenantUsersWith: {username: %q}}}}`, admin),
			fmt.Sprintf(`{hasUserWith: {hasTenantWith: {hasTenantUsersWith: {email: %q}}}}`, b.Fixture.AdminEmail),
		} {
			res := s.gql(a.PAT, a.RT.ID, fmt.Sprintf(`{ deviceUsers(where: %s) { totalCount edges { node { id } } } }`, where), nil)
			s.Require().False(res.failed(), res.errText())
			s.NotContains(connIDs(res.Data, "deviceUsers"), guestDU,
				"deviceUsers(where: %s) matched through B's tenant row", where)
		}
	})

	s.Run("plain hasDeviceUsersUsers: B's user does not match through A's row", func() {
		res := s.gql(b.PAT, b.RT.ID, fmt.Sprintf(`{ users(where: {id: %q, hasDeviceUsersUsers: true}) { totalCount } }`, b.UserID), nil)
		s.Require().False(res.failed(), res.errText())
		s.Equal(0, count(res.Data, "users"), "B learned that its user is checked in somewhere in another tenant")
	})

	s.Run("positive control: A's plain hasDeviceUsersUsers matches A's own user", func() {
		res := s.gql(a.PAT, a.RT.ID, fmt.Sprintf(`{ users(where: {id: %q, hasDeviceUsersUsers: true}) { totalCount } }`, a.UserID), nil)
		s.Require().False(res.failed(), res.errText())
		s.Equal(1, count(res.Data, "users"))
	})

	s.Run("B does not see A's device_users row", func() {
		res := s.gql(b.PAT, b.RT.ID, fmt.Sprintf(`{ deviceUsers(where: {id: %q}) { totalCount } }`, guestDU), nil)
		s.Require().False(res.failed(), res.errText())
		s.Equal(0, count(res.Data, "deviceUsers"))
	})
}

// createLocation creates a location in the tenant and returns its id.
func (s *RelationFilterSuite) createLocation(t *tenantCtx, name string) string {
	s.T().Helper()
	d := s.ok(t.PAT, t.RT.ID, `mutation($n: String!) { createLocation(input: {name: $n}) { location { id } } }`,
		map[string]any{"n": name})
	id := digStr(d, "createLocation", "location", "id")
	s.Require().NotEmpty(id, "createLocation returned no id")
	return id
}

// createDevice creates a device in the tenant, places it at locationID and
// returns its id.
func (s *RelationFilterSuite) createDevice(t *tenantCtx, name, locationID string) string {
	s.T().Helper()
	d := s.ok(t.PAT, t.RT.ID, `mutation($n: String!) { createDevice(input: {name: $n}) { device { id } } }`,
		map[string]any{"n": name})
	id := digStr(d, "createDevice", "device", "id")
	s.Require().NotEmpty(id, "createDevice returned no id")
	s.ok(t.PAT, t.RT.ID, `mutation($d: ID!, $l: ID!) { setDeviceLocation(input: {deviceID: $d, locationID: $l}) { DeviceLocation { id } } }`,
		map[string]any{"d": id, "l": locationID})
	return id
}

// connIDs returns the node ids of a relay connection at path.
func connIDs(v any, path ...string) []string {
	edges, _ := dig(v, append(path, "edges")...).([]any)
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		if id := digStr(e, "node", "id"); id != "" {
			out = append(out, id)
		}
	}
	return out
}
