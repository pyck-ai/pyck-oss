//go:build integration

package tenants

import (
	"fmt"
	"time"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// TnExpirySuite walks a single tenant through the expiry-driven
// soft-delete and the subsequent restore. The tenant is registered
// without an expiry, fully provisioned and baseline-probed first, and
// only then given a near-future expiry via setTenantExpiry — so
// provisioning latency can never race the sweep into disabling the
// tenant before the baseline has proven it accepted. tenant-expiry-check
// finds the expired tenant and sets deleted_at via the same path the
// deleteTenant resolver uses; the resulting outbox/NATS event drives
// DisableTenantWorkflow + authn.SubscribeRevocations eviction across
// every backend service.
//
// We never trigger a workflow ourselves on the expiry side — the sweep
// observes the expiry on its own cadence, controlled by
// PYCK_TENANT_EXPIRY_CHECK_INTERVAL (s.Cfg.TenantExpiryCheckInterval;
// service default 1m, local .env override 5s).
//
// Coverage:
//   - register tenant, provision PAT, baseline acceptance across every
//     probed service
//   - setTenantExpiry with a near-future expiry
//   - expiry-check sweep → tenant disabled, PAT rejected by every
//     service, Zitadel org INACTIVE
//   - quiescence: the org stays INACTIVE through 1.5 more sweep
//     intervals (the sweep must not re-fire for already-disabled
//     tenants)
//   - restore + cleared expiry → Zitadel org ACTIVE again, same PAT
//     accepted by every service (validates the org-active check on the
//     auth path flips back to active and the cache rebuilds correctly)
type TnExpirySuite struct {
	tests.Base
}

func (s *TnExpirySuite) TestExpiry() {
	tenant := fixtures.NewTenant()
	s.T().Logf("tenant=%s admin=%s", tenant.Name, tenant.AdminUsername)

	// expiryFlowBudget bounds the wait for the sweep-driven disable: at
	// most one full sweep interval until the expiry is noticed, plus
	// revocationTimeout for the disable to propagate. The latter must be
	// the full revocation bound, not a small margin: the baseline phase
	// just cached a POSITIVE org verdict in every service, so a service
	// that misses the NATS eviction keeps accepting until that cache
	// entry expires (the 60s TTL backstop revocationTimeout is sized to
	// clear).
	expiryFlowBudget := s.Cfg.TenantExpiryCheckInterval + revocationTimeout

	var (
		registered *gateway.RegisteredTenant
		pat        string
	)

	// The tenant starts without an expiry; that is set in its own phase
	// once the baseline holds. The cleanup is registered here — not as a
	// final phase — so an early bail-out in any later phase still tears
	// the tenant down; on the success path the tenant is active again,
	// so the cleanup does the real disable.
	if !s.Run("register tenant", func() {
		r := s.Require()
		rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
		r.NoError(err)
		r.NotEmpty(rt.IdpOrgRef)
		registered = rt
		s.DeferTenantCleanup(rt.ID)
		s.T().Logf("tenantID=%s idpOrgRef=%s", rt.ID, rt.IdpOrgRef)
	}) {
		return
	}

	// Provisions a machine user + grant + PAT and blocks until Zitadel's
	// projections accept the PAT, so the baseline phase below never
	// races the PAT projection.
	if !s.Run("provision machine user and PAT", func() {
		r := s.Require()
		p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, registered, tests.RolesWithServiceGates("writer"))
		r.NoError(err)
		pat = p.PAT
		s.T().Logf("userID=%s", p.UserID)
	}) {
		return
	}

	// Confirms the test setup is sound before any expiry exists. If the
	// PAT were rejected here it would be a baseline failure, not an
	// expiry-check success — distinguishing the two avoids false greens.
	if !s.Run("baseline: PAT accepted by all services", func() {
		r := s.Require()
		_, err := waitUntilAllAccept(s.Ctx, s.Cfg, pat, 30*time.Second)
		r.NoError(err)
	}) {
		return
	}

	// Only now does the tenant get its expiry, a few seconds out: the
	// sweep cannot have disabled anything before the baseline passed,
	// and the short horizon keeps the wait for the sweep minimal.
	if !s.Run("set short expiry", func() {
		r := s.Require()
		expiresAt := time.Now().UTC().Add(5 * time.Second)
		r.NoError(gateway.SetTenantExpiry(s.Ctx, s.Cfg, registered.ID, &expiresAt))
		s.T().Logf("expiresAt=%s", expiresAt.Format(time.RFC3339))
	}) {
		return
	}

	// The full chain: the expiry-check sweep finds expires_at in the
	// past → soft-delete activity sets deleted_at via Ent (outbox row) →
	// OutboxHandler publishes NATS → tenant-lifecycle subscriber starts
	// DisableTenantWorkflow → Zitadel goes INACTIVE; in parallel each
	// service's authn.SubscribeRevocations consumer evicts the tenant's
	// cached tokens, forcing re-introspection that fails because the
	// org is INACTIVE.
	if !s.Run("expiry triggers soft-delete end-to-end", func() {
		r := s.Require()

		latencies, err := waitUntilAllReject(s.Ctx, s.Cfg, pat, expiryFlowBudget)
		r.NoError(err)
		for name, d := range latencies {
			s.T().Logf("%s rejected after %s", name, d.Round(time.Millisecond))
		}

		took, err := zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, registered.IdpOrgRef, "INACTIVE", expiryFlowBudget)
		r.NoError(err)
		s.T().Logf("Zitadel org INACTIVE after %s", took.Round(time.Millisecond))
	}) {
		return
	}

	// Quiescence: the sweep must observe deleted_at != nil and skip the
	// already-disabled tenant. A "nothing happens" window has to poll
	// the invariant for its full length — sleeping through it and
	// checking once could sample between a flip and its detection. The
	// window spans 1.5 sweep intervals so at least one more sweep is
	// guaranteed to fire inside it; the phase fails the moment the org
	// leaves INACTIVE.
	if !s.Run("idempotency after expiry", func() {
		r := s.Require()
		// The window spans 1.5 sweep intervals so at least one more tick
		// fires with the tenant already soft-deleted; the org must hold
		// INACTIVE through all of it. A transient OrgState fault says
		// nothing about the invariant, so the sample is skipped rather
		// than failing the window — the authoritative re-check below
		// covers the (pathological) case of every sample erroring.
		r.NoError(tests.PollStable(s.Ctx, s.Cfg.TenantExpiryCheckInterval*3/2, time.Second, func() error {
			state, err := zitadelclient.OrgState(s.Ctx, s.ZConn, registered.IdpOrgRef)
			if err != nil {
				s.T().Logf("quiescence sample skipped (org state: %v)", err)
				return nil
			}
			if state != "INACTIVE" {
				return fmt.Errorf("Zitadel org flipped to %q during the quiescence window — expiry sweep re-fired on an already-disabled tenant", state)
			}
			return nil
		}), "org must stay INACTIVE for the whole quiescence window")

		state, err := zitadelclient.OrgState(s.Ctx, s.ZConn, registered.IdpOrgRef)
		r.NoError(err)
		r.Equal("INACTIVE", state, "org must still be INACTIVE after the quiescence window")
	}) {
		return
	}

	// Restore with a far-future expiry in ONE mutation: restoreTenant
	// alone would leave the long-past expires_at in place, making the
	// tenant sweep-eligible again the instant deleted_at clears — a
	// sweep tick landing in a restore-then-clear-expiry gap re-disables
	// it (setTenantExpiry can't run first either: the tenant is still
	// soft-deleted). RestoreTenantWithExpiry overwrites expires_at on
	// the same UpdateOneID, so there is no eligible window at all. The
	// outbox event from the restore drives RestoreTenantWorkflow which
	// reactivates the Zitadel org; in parallel each service's
	// SubscribeRevocations consumer is a no-op (the cached entry was
	// already evicted on disable), so the next request introspects
	// fresh, the organization validator returns active, and the same
	// PAT is accepted again.
	s.Run("restore reactivates tenant and PAT works again", func() {
		r := s.Require()
		farFuture := time.Now().Add(24 * time.Hour)
		r.NoError(gateway.RestoreTenantWithExpiry(s.Ctx, s.Cfg, registered.ID, &farFuture))

		took, err := zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, registered.IdpOrgRef, "ACTIVE", 30*time.Second)
		r.NoError(err)
		s.T().Logf("Zitadel org ACTIVE after %s", took.Round(time.Millisecond))

		latencies, err := waitUntilAllAccept(s.Ctx, s.Cfg, pat, 30*time.Second)
		r.NoError(err)
		for name, d := range latencies {
			s.T().Logf("%s accepted after %s", name, d.Round(time.Millisecond))
		}
	})
}
