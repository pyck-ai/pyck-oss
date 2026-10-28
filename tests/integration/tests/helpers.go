//go:build integration

// Package tests holds shared test infrastructure used by every
// integration suite under tests/<feature>/. Suite-specific helpers
// stay private to their feature folder.
package tests

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/suite"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"

	natsgo "github.com/pyck-ai/pyck/backend/common/nats"
	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"

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
// or its tenant failed the liveness check; the per-service role gate replies 403
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
	return ProvisionUserInTenantAs(ctx, cfg, conn, rt, fixtures.NewMachineUser(), roles)
}

// ProvisionUserInTenantAs is ProvisionUserInTenant against a caller-chosen
// machine user. Suites want a fresh random one per run; a durable fixture
// wants a stable name, because EnsureMachineUser is idempotent on it and a
// generated name would leave a new user behind on every re-run.
func ProvisionUserInTenantAs(ctx context.Context, cfg *config.Config, conn *zitadel.Connection, rt *gateway.RegisteredTenant, machine *fixtures.MachineUser, roles []string) (*ProvisionedTenant, error) {
	uid, err := zitadelclient.EnsureMachineUser(ctx, conn, rt.IdpOrgRef, machine)
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
		deleteTenant(b.T().Logf, b.Cfg, id)
	}
}

// DeferSuiteTenantCleanup soft-deletes a tenant SetupSuite registered when the
// suite ends, even if SetupSuite fails (testify defers TearDownSuite only
// after SetupSuite returns). Call it from SetupSuite only.
func (b *Base) DeferSuiteTenantCleanup(tenantID string) {
	t, cfg := b.T(), b.Cfg
	t.Cleanup(func() { deleteTenant(t.Logf, cfg, tenantID) })
}

func deleteTenant(logf func(string, ...any), cfg *config.Config, tenantID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := gateway.DeleteTenant(ctx, cfg, tenantID); err != nil {
		logf("cleanup: deleteTenant %s: %v (already disabled by the test itself is fine)", tenantID, err)
	}
}

// DialTenantNATS connects through the auth callout as a client of tenantID, on
// its inbox prefix. It does not reconnect, so a refusal stays visible.
func DialTenantNATS(cfg *config.Config, token string, tenantID uuid.UUID, opts ...nats.Option) (*nats.Conn, error) {
	opts = append([]nats.Option{
		nats.Token(token),
		nats.CustomInboxPrefix(natsgo.TenantInboxPrefix(tenantID)),
		nats.NoReconnect(),
	}, opts...)

	nc, err := nats.Connect(cfg.NatsWSURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("connect tenant %s to %s through the auth callout: %w", tenantID, cfg.NatsWSURL, err)
	}

	return nc, nil
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

// WaitForSyncedUser polls management (system token) until the user with the
// given username is projected into the tenant by the zitadel-sync workflow,
// returning its management user id. The lookup filters by username
// server-side, so it stays O(1) however many users the tenant accumulates.
//
// The budget is cfg.UserSyncWaitTimeout, floored at one sync cadence plus
// slack: registration triggers a sync for the tenant's admin, but a user
// provisioned afterwards (ProvisionUserInTenant) reaches management.users
// through the scheduled sweep alone (no mutation or admin endpoint can
// force it) so on a stack running the 10-minute service default the wait
// is that long, not the 30 s a test-cadence stack needs.
func WaitForSyncedUser(ctx context.Context, cfg *config.Config, tenantID, username string) (string, error) {
	c := gateway.NewClient(cfg, cfg.ServiceToken)
	first := 1
	var userID string
	timeout := max(cfg.UserSyncWaitTimeout, cfg.ZitadelSyncInterval+30*time.Second)
	err := PollUntil(ctx, timeout, 500*time.Millisecond, func() error {
		resp, err := c.GetUsers(ctx, managementapi.GetUsersArgs{
			First: &first,
			Where: &managementapi.UserWhereInput{TenantID: &tenantID, Username: &username},
		})
		if err != nil {
			return fmt.Errorf("getUsers: %w", err)
		}
		users := resp.GetUsers()
		if users == nil || len(users.GetEdges()) == 0 || users.GetEdges()[0].GetNode() == nil {
			return fmt.Errorf("user %q not yet synced into tenant %s", username, tenantID)
		}
		userID = users.GetEdges()[0].GetNode().GetID()
		return nil
	})
	return userID, err
}

// ErrTenantNotVisible is returned by TenantStatus when the tenants query
// does not surface the requested tenant for the given token.
var ErrTenantNotVisible = errors.New("tenant not visible to token")

// TenantStatus reads the registration status of the given tenant through
// the tenants query with the given bearer token, the second vantage point
// on what registerTenant reported. Returns ErrTenantNotVisible when the
// tenant is not visible to the token.
func TenantStatus(ctx context.Context, cfg *config.Config, token, tenantID string) (string, error) {
	node, err := tenantNode(ctx, cfg, token, tenantID)
	if err != nil {
		return "", err
	}
	return node.GetStatus().String(), nil
}

// TenantExpiresAt reads the tenant's expires_at through the tenants query
// with the given bearer token, the read side of setTenantExpiry and of
// restoreTenant's expiry override. A nil time is a tenant with no expiry,
// which is what a cleared one must read as.
func TenantExpiresAt(ctx context.Context, cfg *config.Config, token, tenantID string) (*time.Time, error) {
	node, err := tenantNode(ctx, cfg, token, tenantID)
	if err != nil {
		return nil, err
	}
	return node.GetExpiresAt(), nil
}

// tenantNode reads one tenant row through the tenants query. Returns
// ErrTenantNotVisible when the tenant is not visible to the token, which
// is also how a soft-deleted tenant reads without the show-deleted feature.
func tenantNode(ctx context.Context, cfg *config.Config, token, tenantID string) (*managementapi.GetTenants_Tenants_Edges_Node, error) {
	c := gateway.NewClient(cfg, token)
	first := 1
	resp, err := c.GetTenants(ctx, managementapi.GetTenantsArgs{
		First: &first,
		Where: &managementapi.TenantWhereInput{ID: &tenantID},
	})
	if err != nil {
		return nil, fmt.Errorf("getTenants: %w", err)
	}
	for _, edge := range resp.GetTenants().GetEdges() {
		if edge == nil || edge.GetNode() == nil || edge.GetNode().GetID() != tenantID {
			continue
		}
		return edge.GetNode(), nil
	}
	return nil, fmt.Errorf("%w: %s", ErrTenantNotVisible, tenantID)
}

// ErrStillAccepted is the poll error WaitUntilDenied reports while the probe
// keeps being accepted; on timeout it is what the caller sees wrapped.
var ErrStillAccepted = errors.New("still accepted")

// WaitUntilDenied polls probe until it fails with a definitive auth denial
// (IsAuthDenial), meaning introspection, the home-tenant liveness check or
// the acting-tenant check has caught up with a revocation. Acceptance keeps
// polling; so do non-auth errors (transport faults, gateway 5xx): counting
// them as denial would certify a revocation no service ever performed.
// `what` names the probe in the timeout error.
func WaitUntilDenied(ctx context.Context, what string, timeout time.Duration, probe func() error) (time.Duration, error) {
	return PollUntilElapsed(ctx, timeout, 200*time.Millisecond, func() error {
		err := probe()
		switch {
		case err == nil:
			return fmt.Errorf("%s: %w", what, ErrStillAccepted)
		case IsAuthDenial(err.Error()):
			return nil
		default:
			return fmt.Errorf("%s probe infra error (not a denial): %w", what, err)
		}
	})
}

// WaitUntilAccepted polls probe until it returns nil — the barrier for
// "the grant / PAT / restore has propagated". Every probe error, auth or
// infra, keeps polling; the last one is reported on timeout.
func WaitUntilAccepted(ctx context.Context, what string, timeout time.Duration, probe func() error) (time.Duration, error) {
	return PollUntilElapsed(ctx, timeout, 200*time.Millisecond, func() error {
		if err := probe(); err != nil {
			return fmt.Errorf("%s not accepted yet: %w", what, err)
		}
		return nil
	})
}

// WaitUntilTokenDenied is WaitUntilDenied over TenantInList with the given
// bearer token: used after RemovePAT / DeleteMachineUser, where rejection
// lags the Zitadel API call by however long the validation cache TTL is.
func WaitUntilTokenDenied(ctx context.Context, cfg *config.Config, token, tenantID string, timeout time.Duration) (time.Duration, error) {
	return WaitUntilDenied(ctx, "token", timeout, func() error {
		_, err := TenantInList(ctx, cfg, token, tenantID)
		return err
	})
}

// WaitTokenReady is WaitUntilAccepted over TenantInList, additionally
// requiring the token's own tenant to be visible — i.e. the freshly-minted
// PAT and its project grant have both projected through Zitadel's
// eventually consistent introspection path. Used right after provisioning
// a tenant-scoped PAT, before driving real writes with it.
func WaitTokenReady(ctx context.Context, cfg *config.Config, token, tenantID string, timeout time.Duration) (time.Duration, error) {
	return WaitUntilAccepted(ctx, "token", timeout, func() error {
		ok, err := TenantInList(ctx, cfg, token, tenantID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("tenant %s not visible to token yet", tenantID)
		}
		return nil
	})
}

// composeDBContainer is the compose service name of the stack's Postgres.
const composeDBContainer = "db"

// Psql runs one SQL statement in the stack's database through docker exec
// and returns its unaligned, tuples-only output with surrounding
// whitespace trimmed. For setting up or reading state no API exposes: a
// tenant row in a status only a crashed registration leaves behind, or a
// soft-deleted row the API hides. Never for anything an API can do.
func Psql(ctx context.Context, query string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "exec", "-i", composeDBContainer,
		"psql", "-U", "admin", "-d", "pyck_dev", "-v", "ON_ERROR_STOP=1", "-tA", "-c", query).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("psql: %w: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}
