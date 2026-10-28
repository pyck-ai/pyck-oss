package nats

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/liveness"
	livenessmocks "github.com/pyck-ai/pyck/backend/common/liveness/mocks"
	"github.com/pyck-ai/pyck/backend/common/tenant"
)

// newTestAuthService builds an authService with only the fields
// authorizeTenantSelection reads; the NATS-facing fields (key pair,
// authenticator) stay nil because the decision under test runs after
// Authenticate and before signing.
func newTestAuthService(t *testing.T, source liveness.TenantSource) *authService {
	t.Helper()
	return newAuthService(t.Context(), "pyck-stream", "pyck", nil, nil, livenessmocks.NewTestCache(t), source)
}

func tenantHeader(ids ...uuid.UUID) http.Header {
	header := http.Header{}
	for _, id := range ids {
		header.Add(tenant.TenantIDHeader, id.String())
	}
	return header
}

func readerOn(tenantIDs ...uuid.UUID) authn.User {
	roles := make(map[uuid.UUID]authn.Role, len(tenantIDs))
	for _, id := range tenantIDs {
		roles[id] = authn.ROLE_READER
	}
	return authn.User{
		ID:       uuid.New(),
		TenantID: tenantIDs[0],
		Username: "user",
		Roles:    roles,
	}
}

func TestAuthorizeTenantSelection_LiveTenantWithRole(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	service := newTestAuthService(t, (&livenessmocks.Source{}).Lookup)

	got, err := service.authorizeTenantSelection(t.Context(), readerOn(tenantID), tenantHeader(tenantID))
	require.NoError(t, err)
	assert.Equal(t, tenantID, got)
}

// A selection that does not resolve to exactly one tenant must be refused
// with an error, not reach MutationTenantID: the accessor panics there,
// and Handle's recover would swallow the panic into an uninformative 500.
func TestAuthorizeTenantSelection_MultiTenantSelectionRefused(t *testing.T) {
	t.Parallel()

	tenant1 := uuid.New()
	tenant2 := uuid.New()
	service := newTestAuthService(t, (&livenessmocks.Source{}).Lookup)

	_, err := service.authorizeTenantSelection(t.Context(), readerOn(tenant1, tenant2), tenantHeader(tenant1, tenant2))
	require.ErrorIs(t, err, errTenantNotSingle)
}

// Naming a tenant in the connect headers grants nothing without a role in
// it: any authenticated user could otherwise scope its NATS permissions to
// an arbitrary tenant's stream subjects.
func TestAuthorizeTenantSelection_NoRoleRefused(t *testing.T) {
	t.Parallel()

	foreignTenant := uuid.New()
	source := &livenessmocks.Source{}
	service := newTestAuthService(t, source.Lookup)

	user := readerOn(uuid.New())
	_, err := service.authorizeTenantSelection(t.Context(), user, tenantHeader(foreignTenant))
	require.ErrorIs(t, err, tenant.ErrNoAccessToTenantID)
	assert.Zero(t, source.Calls(),
		"the role check must run before the liveness lookup: the callout must not be a tenant-state oracle for the unauthorized")
}

// A tenant no live row backs is refused even with a role: its retained
// JetStream history must not stay readable through a stale grant.
func TestAuthorizeTenantSelection_DeadTenantRefused(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	source := &livenessmocks.Source{Disabled: map[uuid.UUID]struct{}{tenantID: {}}}
	service := newTestAuthService(t, source.Lookup)

	_, err := service.authorizeTenantSelection(t.Context(), readerOn(tenantID), tenantHeader(tenantID))
	require.ErrorIs(t, err, errTenantNotServed)
}

// A source fault denies a tenant with no cached verdict: a connection that
// cannot be verified is not granted tenant-scoped permissions; failing
// open here would hand a deleted tenant's retained stream history to a
// holdover token for the length of a source outage.
func TestAuthorizeTenantSelection_SourceErrorDeniesUncached(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	source := &livenessmocks.Source{Err: assert.AnError}
	service := newTestAuthService(t, source.Lookup)

	_, err := service.authorizeTenantSelection(t.Context(), readerOn(tenantID), tenantHeader(tenantID))
	require.ErrorIs(t, err, errTenantNotServed)
}

// A cached live verdict keeps admitting connections through a source fault
// for its TTL: the deny-on-unknown rule must not take down tenants this
// replica has already verified.
func TestAuthorizeTenantSelection_CachedLiveVerdictHoldsThroughFault(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	source := &livenessmocks.Source{}
	cache := livenessmocks.NewTestCache(t)
	_, err := cache.LookupTenants(t.Context(), []uuid.UUID{tenantID}, source.Lookup)
	require.NoError(t, err)
	source.Err = assert.AnError

	service := newAuthService(t.Context(), "pyck-stream", "pyck", nil, nil, cache, source.Lookup)
	got, err := service.authorizeTenantSelection(t.Context(), readerOn(tenantID), tenantHeader(tenantID))
	require.NoError(t, err)
	assert.Equal(t, tenantID, got)
}

// System users keep their historical derivation bit-for-bit: the resolved
// selection scopes them, with no role check and no liveness check; they
// are how platform services connect, and the liveness source is itself
// reached through a system token.
func TestAuthorizeTenantSelection_SystemUserSkipsChecks(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	source := &livenessmocks.Source{Disabled: map[uuid.UUID]struct{}{tenantID: {}}}
	service := newTestAuthService(t, source.Lookup)

	got, err := service.authorizeTenantSelection(t.Context(), *authn.SystemUser(), tenantHeader(tenantID))
	require.NoError(t, err)
	assert.Equal(t, tenantID, got)
	assert.Zero(t, source.Calls(), "a system user's connection must never consult the tenant source")
}

// A system user's selection must also resolve to exactly one tenant: the
// guard runs before MutationTenantID for every caller, so a platform
// service connecting with a doubled or missing tenant header gets the
// named 401 instead of the accessor's panic swallowed into a 500.
func TestAuthorizeTenantSelection_SystemUserAmbiguousSelectionRefused(t *testing.T) {
	t.Parallel()

	tenant1 := uuid.New()
	tenant2 := uuid.New()
	service := newTestAuthService(t, (&livenessmocks.Source{}).Lookup)

	_, err := service.authorizeTenantSelection(t.Context(), *authn.SystemUser(), tenantHeader(tenant1, tenant2))
	require.ErrorIs(t, err, errTenantNotSingle)
}

// A tenant this replica already knows is dead stays refused while the
// source is down: the cached verdict is authoritative, and an
// infrastructure fault must not reopen access a disable already revoked.
func TestAuthorizeTenantSelection_CachedDisabledVerdictHoldsThroughFault(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	source := &livenessmocks.Source{Disabled: map[uuid.UUID]struct{}{tenantID: {}}}
	cache := livenessmocks.NewTestCache(t)
	_, err := cache.LookupTenants(t.Context(), []uuid.UUID{tenantID}, source.Lookup)
	require.NoError(t, err)
	source.Err = assert.AnError

	service := newAuthService(t.Context(), "pyck-stream", "pyck", nil, nil, cache, source.Lookup)
	_, err = service.authorizeTenantSelection(t.Context(), readerOn(tenantID), tenantHeader(tenantID))
	require.ErrorIs(t, err, errTenantNotServed)
}

// A stalled source (an accepted connection that never answers) must not
// wedge the serial callout handler: the lookup's own deadline
// (liveness.DefaultLookupTimeout) turns the stall into a bounded refusal long
// before the liveness package's 10s detached-call bound, so queued
// connection attempts are not serialized behind one cold key past the
// NATS server's callout deadline.
func TestAuthorizeTenantSelection_StalledSourceRefusedWithinLookupDeadline(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stalled := func(ctx context.Context, _ []uuid.UUID) ([]uuid.UUID, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, context.DeadlineExceeded
	}
	service := newTestAuthService(t, stalled)

	start := time.Now()
	_, err := service.authorizeTenantSelection(t.Context(), readerOn(tenantID), tenantHeader(tenantID))
	require.ErrorIs(t, err, errTenantNotServed)
	assert.Less(t, time.Since(start), 3*time.Second,
		"the callout's own deadline must refuse long before the 10s detached-source bound")
}

// A tenant user's minted credential expires after defaultUserJWTTTL: the callout
// runs only at connect time, so the expiry is what forces a connected
// client back through the role and liveness checks after its tenant is
// disabled; without it a standing connection would outlast the disable
// indefinitely, retained stream history included.
func TestMintUserClaims_TenantUserExpires(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	service := newTestAuthService(t, livenessmocks.NoDisabledTenants)

	before := time.Now()
	claims := service.mintUserClaims("UAUSERNKEY", tenantID, false)

	require.NotZero(t, claims.Expires)
	assert.GreaterOrEqual(t, claims.Expires, before.Add(defaultUserJWTTTL-userJWTTTLJitter/2).Unix(),
		"expiry must sit inside the jitter window around defaultUserJWTTTL")
	assert.LessOrEqual(t, claims.Expires, time.Now().Add(defaultUserJWTTTL+userJWTTTLJitter/2).Unix())
	assert.Equal(t, tenantID.String(), claims.Name)
	assert.Contains(t, claims.Pub.Allow, "pyck-stream."+tenantID.String()+".>")
	assert.Equal(t, []string{"_INBOX." + tenantID.String() + ".>"}, []string(claims.Sub.Allow))
}

// System-user credentials keep their historical, non-expiring shape:
// their access is not derived from tenant liveness, and expiring them
// would put every platform service on a reconnect cadence for no added
// enforcement.
func TestMintUserClaims_SystemUserDoesNotExpire(t *testing.T) {
	t.Parallel()

	service := newTestAuthService(t, livenessmocks.NoDisabledTenants)

	claims := service.mintUserClaims("UAUSERNKEY", uuid.New(), true)
	assert.Zero(t, claims.Expires)
}

// natsServerConfPath is the server config the local stack and the compose
// image run; TestConfiguredAccountMatchesNatsConf pins the account name in
// it the same way.
const natsServerConfPath = "../../../config/nats/nats.conf"

// The server abandons a callout it has waited on for its authorization
// timeout. A handler budget at or above that deadline is answered to a
// server that stopped listening, and because the callout endpoint serves
// requests serially the abandoned work delays every connection queued
// behind it: a slow dependency then collapses connects for everyone
// instead of just slowing them. Nothing in the build ties the two numbers
// together, so this test does.
//
// Per request only. The endpoint dispatches serially, so N callouts that
// each burn the full budget make the Nth reply at roughly N x
// defaultCalloutTimeout, past the server deadline however this test reads;
// queue depth is a separate property this hierarchy does not bound.
func TestCalloutBudgetFitsNatsConfTimeout(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(natsServerConfPath)
	if err != nil {
		// Not shipped with the module; skip outside a repo checkout.
		t.Skipf("cannot read %s: %v", natsServerConfPath, err)
	}

	serverTimeout := authorizationTimeout(t, string(raw))
	assert.Greater(t, serverTimeout, defaultCalloutTimeout,
		"nats.conf authorization timeout (%s) must exceed defaultCalloutTimeout (%s)", serverTimeout, defaultCalloutTimeout)
	assert.Greater(t, defaultCalloutTimeout, liveness.DefaultLookupTimeout,
		"defaultCalloutTimeout (%s) must leave room for the liveness lookup (%s) and the signed refusal",
		defaultCalloutTimeout, liveness.DefaultLookupTimeout)
}

// authorizationTimeout reads the NATS server's authorization timeout, in
// seconds, from the authorization block of conf. Keys of nested blocks
// (auth_callout) are skipped, so a timeout added inside one cannot be
// mistaken for the block's own.
func authorizationTimeout(t *testing.T, conf string) time.Duration {
	t.Helper()

	start := strings.Index(conf, "authorization")
	require.GreaterOrEqual(t, start, 0, "nats.conf declares no authorization block")
	rest := conf[start:]
	open := strings.Index(rest, "{")
	require.GreaterOrEqual(t, open, 0, "the authorization block has no body")

	var body strings.Builder
	depth := 0
	for _, r := range rest[open:] {
		switch {
		case r == '{':
			depth++
		case r == '}':
			depth--
			if depth == 0 {
				body.WriteRune('\n')
			}
		case depth == 1:
			body.WriteRune(r)
		}
		if depth == 0 {
			break
		}
	}

	match := regexp.MustCompile(`(?m)^\s*timeout:\s*([0-9.]+)`).FindStringSubmatch(body.String())
	require.NotNil(t, match, "the authorization block declares no timeout of its own")
	seconds, err := strconv.ParseFloat(match[1], 64)
	require.NoError(t, err, "authorization timeout %q is not a number", match[1])
	return time.Duration(seconds * float64(time.Second))
}
