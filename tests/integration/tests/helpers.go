//go:build integration

// Package tests holds shared test infrastructure used by every
// integration suite under tests/<feature>/. Suite-specific helpers
// stay private to their feature folder.
package tests

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	"github.com/stretchr/testify/suite"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"

	"github.com/pyck-ai/pyck/tests/integration/internal/config"
	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
)

// PollUntil retries fn until it returns nil, sleeping interval between
// attempts. On timeout the returned error wraps fn's last error so callers
// see why the condition never held; ctx cancellation unwinds promptly
// instead of polling to the deadline. Every eventually-consistent wait in
// the suites (Zitadel projections, Temporal visibility, NATS-driven state)
// goes through this — a fixed sleep is forbidden (see AGENTS.md).
func PollUntil(ctx context.Context, timeout, interval time.Duration, fn func() error) error {
	_, err := PollUntilElapsed(ctx, timeout, interval, fn)
	return err
}

// PollUntilElapsed is PollUntil, additionally reporting how long the
// condition took to hold — for suites that assert on propagation latency.
func PollUntilElapsed(ctx context.Context, timeout, interval time.Duration, fn func() error) (time.Duration, error) {
	start := time.Now()
	deadline := start.Add(timeout)
	var lastErr error
	for {
		err := fn()
		if err == nil {
			return time.Since(start), nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("timeout after %s: %w", timeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// PollStable is the quiescence counterpart to PollUntil: it asserts an
// invariant HOLDS for the whole window, running check every interval and
// returning the first non-nil error immediately — the moment the invariant
// breaks — and nil once the window closes with every check green. Use it for
// "nothing happens" phases (a sweeper must NOT re-fire, a state must NOT
// flip); sleeping the window and checking once would miss transient flips,
// and bending PollUntil around inverted nil-means-broken semantics reads
// backwards.
func PollStable(ctx context.Context, window, interval time.Duration, check func() error) error {
	deadline := time.Now().Add(window)
	for {
		if err := check(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// IsAuthDenial reports whether a gateway error message is a definitive
// auth/tenant denial rather than an infrastructure fault. Every denial
// funnels through the subgraphs' shared auth middleware
// (backend/common/authn HTTPMiddleware), which replies HTTP 401 with
// http.StatusText — "Unauthorized" — whether the token is invalid, revoked,
// or its org failed the OrgValidator; the per-service role gate replies 403
// "Forbidden". Apollo Router (include_subgraph_errors: all) folds those
// non-200s into GraphQL errors embedding the subgraph status line, e.g.
// "HTTP fetch failed from 'inventory': 401: Unauthorized". Anything else —
// transport failures, 5xx, resolver errors — says nothing about the token
// and must never be counted as a rejection.
//
// Deliberately matches the status-text words, NOT bare "401"/"403" digits:
// a bare-digit match fires on hex IDs, ports, and byte counts embedded in
// unrelated error messages, silently latching a service as revoked while it
// still accepts the token.
func IsAuthDenial(msg string) bool {
	return strings.Contains(msg, "Unauthorized") ||
		strings.Contains(msg, "Forbidden")
}

// RolesWithServiceGates returns the given ladder roles (e.g. "writer") plus
// every per-service gate role. A PAT granted these satisfies both the
// reader/writer/admin ladder AND the per-service gate for all gated services,
// so it can drive real reads/writes across the federated subgraphs. Use it in
// any suite that provisions a PAT and then talks to a gated service
// (inventory, picking, receiving, file, main-data) through the gateway — the
// gate is orthogonal to the ladder and a ladder role alone no longer opens a
// gated service.
func RolesWithServiceGates(ladderRoles ...string) []string {
	roles := append([]string(nil), ladderRoles...)
	for _, r := range serviceroles.All {
		roles = append(roles, r.String())
	}
	return roles
}

// Base is the common skeleton every integration suite extends. It
// loads config + dials Zitadel once per suite run and tears the gRPC
// connection down at the end.
type Base struct {
	suite.Suite

	Ctx   context.Context
	Cfg   *config.Config
	ZConn *zitadel.Connection

	// cleanupTenants collects tenant IDs registered via DeferTenantCleanup,
	// drained by TearDownTest. Guarded by cleanupMu (suites run serially,
	// but the guard makes the invariant explicit).
	cleanupMu      sync.Mutex
	cleanupTenants []string
}

func (b *Base) SetupSuite() {
	b.Ctx = context.Background()

	cfg, err := config.Load()
	b.Require().NoError(err, "config.Load")
	b.Cfg = cfg

	conn, err := zitadelclient.Dial(b.Ctx, cfg)
	b.Require().NoError(err, "zitadelclient.Dial")
	b.ZConn = conn
}

func (b *Base) TearDownSuite() {
	if b.ZConn != nil {
		_ = b.ZConn.Close()
	}
}

// ProvisionedTenant bundles what ProvisionUserInTenant creates: the
// tenant it ran against, the machine user inside its Zitadel org, and a
// ready-to-use PAT.
type ProvisionedTenant struct {
	Tenant  *gateway.RegisteredTenant
	UserID  string
	TokenID string
	PAT     string
}

// ProvisionUserInTenant runs the user half of the standard provisioning
// recipe against an already-registered tenant: machine user + project grant
// + PAT + readiness barrier (the PAT is usable the moment this returns).
// Pass RolesWithServiceGates("writer") when the suite drives gated services
// through the gateway, or bare ladder roles otherwise. Callers register the
// tenant themselves (the fixture's names are usually logged for debugging)
// and MUST call DeferTenantCleanup right after registration, before this —
// so a run that fails in here still tears its tenant down.
func ProvisionUserInTenant(ctx context.Context, cfg *config.Config, conn *zitadel.Connection, rt *gateway.RegisteredTenant, roles []string) (*ProvisionedTenant, error) {
	uid, err := zitadelclient.EnsureMachineUser(ctx, conn, rt.IdpOrgRef, fixtures.NewMachineUser())
	if err != nil {
		return nil, fmt.Errorf("ensure machine user: %w", err)
	}
	if err := zitadelclient.EnsureProjectGrant(ctx, conn, rt.IdpOrgRef, cfg.ZitadelProjectID, uid, roles); err != nil {
		return nil, fmt.Errorf("ensure project grant: %w", err)
	}
	tokenID, pat, err := zitadelclient.AddPAT(ctx, conn, uid)
	if err != nil {
		return nil, fmt.Errorf("add PAT: %w", err)
	}
	if _, err := WaitTokenReady(ctx, cfg, pat, rt.ID, 30*time.Second); err != nil {
		return nil, fmt.Errorf("PAT never became usable: %w", err)
	}
	return &ProvisionedTenant{Tenant: rt, UserID: uid, TokenID: tokenID, PAT: pat}, nil
}

// DeferTenantCleanup schedules the tenant for soft-deletion when the current
// test method finishes (see TearDownTest). Call it immediately after a
// successful RegisterTenant so even a run that fails mid-provisioning tears
// its tenant down — a cleanup stage at the END of a test is skipped by every
// early return before it.
//
// Deliberately NOT t.Cleanup-based: inside an s.Run stage, s.T() is the
// stage's own *testing.T, so a cleanup registered there fires the moment the
// stage ends — soft-deleting the tenant mid-test while later stages still
// use it. Registering on the suite makes the schedule independent of
// testify's T swapping.
func (b *Base) DeferTenantCleanup(tenantID string) {
	b.cleanupMu.Lock()
	defer b.cleanupMu.Unlock()
	b.cleanupTenants = append(b.cleanupTenants, tenantID)
}

// TearDownTest soft-deletes every tenant the finished test method registered
// via DeferTenantCleanup. Failures are logged, not fatal: the tenant may
// legitimately be disabled already by the test itself, and cleanup must
// never mask the test's own verdict.
func (b *Base) TearDownTest() {
	b.cleanupMu.Lock()
	ids := b.cleanupTenants
	b.cleanupTenants = nil
	b.cleanupMu.Unlock()
	for _, id := range ids {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := gateway.DeleteTenant(ctx, b.Cfg, id); err != nil {
			b.T().Logf("cleanup: deleteTenant %s: %v (already disabled by the test itself is fine)", id, err)
		}
		cancel()
	}
}

// TenantInList queries management.tenants with the given bearer token,
// filtered to the given tenant ID, and reports whether it comes back. Used
// across suites whenever something wants to verify "this token can reach
// management AND sees this tenant". Filtering server-side (rather than
// listing and scanning) keeps it O(1) and immune to the active-tenant count
// growing across runs, where the target could fall outside a fixed page.
func TenantInList(ctx context.Context, cfg *config.Config, token, tenantID string) (bool, error) {
	c := gateway.NewClient(cfg, token)
	first := 1
	resp, err := c.GetTenants(ctx, managementapi.GetTenantsArgs{
		First: &first,
		Where: &managementapi.TenantWhereInput{ID: &tenantID},
	})
	if err != nil {
		return false, fmt.Errorf("getTenants: %w", err)
	}
	t := resp.GetTenants()
	if t == nil {
		return false, nil
	}
	for _, edge := range t.GetEdges() {
		if edge == nil || edge.GetNode() == nil {
			continue
		}
		if edge.GetNode().GetID() == tenantID {
			return true, nil
		}
	}
	return false, nil
}

// WaitUntilTokenRejected polls TenantInList with the given bearer token
// until the request fails with a definitive auth denial (IsAuthDenial) —
// meaning Zitadel introspection (or any downstream auth cache) has caught
// up with a PAT or user deletion. Used after RemovePAT / DeleteMachineUser,
// where rejection lags the Zitadel API call by however long the validation
// cache TTL is. Non-auth errors — transport faults, gateway 5xx — keep
// polling: counting them as rejection would certify a revocation no service
// ever performed.
func WaitUntilTokenRejected(ctx context.Context, cfg *config.Config, token, tenantID string, timeout time.Duration) (time.Duration, error) {
	return PollUntilElapsed(ctx, timeout, 200*time.Millisecond, func() error {
		_, err := TenantInList(ctx, cfg, token, tenantID)
		switch {
		case err == nil:
			return fmt.Errorf("token still accepted")
		case IsAuthDenial(err.Error()):
			return nil
		default:
			return fmt.Errorf("probe infra error (not a rejection): %w", err)
		}
	})
}

// WaitTokenReady polls TenantInList until the given bearer token is
// accepted AND surfaces its own tenant — i.e. the freshly-minted PAT and
// its project grant have both projected through Zitadel's eventually
// consistent introspection path. Used right after provisioning a
// tenant-scoped PAT, before driving real writes with it.
func WaitTokenReady(ctx context.Context, cfg *config.Config, token, tenantID string, timeout time.Duration) (time.Duration, error) {
	return PollUntilElapsed(ctx, timeout, 200*time.Millisecond, func() error {
		ok, err := TenantInList(ctx, cfg, token, tenantID)
		if err != nil {
			return fmt.Errorf("token not ready: %w", err)
		}
		if !ok {
			return fmt.Errorf("tenant %s not visible to token yet", tenantID)
		}
		return nil
	})
}
