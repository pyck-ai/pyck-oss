//go:build integration

package auth

import (
	"time"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// MachineUserSuite walks a single machine user through its PAT
// lifecycle: create, mint two PATs, prove both work, revoke one, prove
// only the survivor works, then delete the user and prove the survivor
// is also rejected once Zitadel propagates the deletion. Exercises
// Zitadel's PAT + user lifecycle as observed through pyck's auth path
// (management.tenants).
//
// Coverage:
//   - register tenant (fixture)
//   - machine user + project grant
//   - PAT1 created and accepted by management
//   - PAT2 added on the same user; both PATs work
//   - PAT1 removed: PAT1 rejected, PAT2 still accepted
//   - machine user deleted: PAT2 also rejected
//   - tenant soft-deleted via DeferTenantCleanup, so it runs even on early failure
//
// Not covered: token validation cache TTL (we poll past it rather than
// measure), grant-removal as a distinct path from user-delete.
type MachineUserSuite struct {
	tests.Base
}

func (s *MachineUserSuite) TestKeyLifecycle() {
	tenant := fixtures.NewTenant()
	machine := fixtures.NewMachineUser()
	s.T().Logf("tenant=%s machine=%s", tenant.Name, machine.Username)

	var (
		registered *gateway.RegisteredTenant
		userID     string
		pat1ID     string
		pat1       string
		pat2       string
	)

	// Register the tenant — gives us the sub-org and management.tenants
	// row that the rest of the lifecycle hangs off of.
	if !s.Run("register tenant", func() {
		r := s.Require()
		rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
		r.NoError(err)
		registered = rt
		s.DeferTenantCleanup(registered.ID)
	}) {
		return
	}

	// Create the machine user under the tenant's sub-org and grant it
	// the writer role on the Pyck project. Without the grant, the
	// machine user has no role for management's auth middleware to
	// accept regardless of how many PATs it holds.
	if !s.Run("create machine user and grant", func() {
		r := s.Require()
		uid, err := zitadelclient.EnsureMachineUser(s.Ctx, s.ZConn, registered.IdpOrgRef, machine)
		r.NoError(err)
		r.NotEmpty(uid)
		userID = uid

		r.NoError(zitadelclient.EnsureProjectGrant(s.Ctx, s.ZConn, registered.IdpOrgRef, s.Cfg.ZitadelProjectID, userID, []string{"writer"}))
	}) {
		return
	}

	// Mint the first PAT and verify it becomes usable against management.
	// The PAT + grant project through Zitadel asynchronously, so this is a
	// bounded poll rather than a single-shot probe: it proves the token is
	// accepted AND surfaces its own tenant, and logs how long that took. If
	// it times out, grant scope or zitadel-sync is off, not a PAT issue.
	if !s.Run("PAT1 accepted by management", func() {
		r := s.Require()
		id, p, err := zitadelclient.AddPAT(s.Ctx, s.ZConn, userID)
		r.NoError(err)
		r.NotEmpty(id)
		r.NotEmpty(p)
		pat1ID = id
		pat1 = p

		took, err := tests.WaitTokenReady(s.Ctx, s.Cfg, pat1, registered.ID, 30*time.Second)
		r.NoError(err, "PAT1 never became usable by management")
		s.T().Logf("PAT1 accepted after %s", took.Round(time.Millisecond))
	}) {
		return
	}

	// Add a second PAT on the same user and verify both work
	// independently. PATs are bearer secrets keyed off the same user;
	// adding one must not invalidate the other.
	if !s.Run("PAT2 added; both work", func() {
		r := s.Require()
		_, p, err := zitadelclient.AddPAT(s.Ctx, s.ZConn, userID)
		r.NoError(err)
		r.NotEmpty(p)
		pat2 = p

		visible, err := tests.TenantInList(s.Ctx, s.Cfg, pat1, registered.ID)
		r.NoError(err, "PAT1 stopped working after minting PAT2")
		r.True(visible, "PAT1 accepted but its tenant is not visible")

		// PAT2 is freshly minted, so its acceptance is polled like PAT1's.
		took, err := tests.WaitTokenReady(s.Ctx, s.Cfg, pat2, registered.ID, 30*time.Second)
		r.NoError(err, "PAT2 never became usable by management")
		s.T().Logf("PAT2 accepted after %s", took.Round(time.Millisecond))
	}) {
		return
	}

	// Remove PAT1 only. Zitadel's introspection / token-validation
	// cache may still accept PAT1 for a short window, so we poll. PAT2
	// must keep working — the removal is per-token, not per-user.
	if !s.Run("delete PAT1; PAT1 rejected, PAT2 still works", func() {
		r := s.Require()
		r.NoError(zitadelclient.RemovePAT(s.Ctx, s.ZConn, userID, pat1ID))

		took, err := tests.WaitUntilTokenRejected(s.Ctx, s.Cfg, pat1, registered.ID, 10*time.Second)
		r.NoError(err)
		s.T().Logf("PAT1 rejected after %s", took.Round(time.Millisecond))

		visible, err := tests.TenantInList(s.Ctx, s.Cfg, pat2, registered.ID)
		r.NoError(err, "PAT2 collateral-rejected when only PAT1 was removed")
		r.True(visible, "PAT2 accepted but its tenant is not visible")
	}) {
		return
	}

	// Delete the machine user. Zitadel cascades this to all of the
	// user's remaining tokens, so PAT2 must also be rejected once the
	// validation cache catches up.
	s.Run("delete machine user; PAT2 rejected", func() {
		r := s.Require()
		r.NoError(zitadelclient.DeleteMachineUser(s.Ctx, s.ZConn, userID))

		took, err := tests.WaitUntilTokenRejected(s.Ctx, s.Cfg, pat2, registered.ID, 10*time.Second)
		r.NoError(err)
		s.T().Logf("PAT2 rejected after %s", took.Round(time.Millisecond))
	})
}
