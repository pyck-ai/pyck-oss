// Package auth provides authentication services using Zitadel.
//
// This package implements authentication and authorization using Zitadel as the
// identity provider. It handles token introspection, user role management, and
// provides HTTP middleware for protecting endpoints.
//
// Key features:
//   - Token introspection with caching for performance
//   - Multi-tenant role management
//   - HTTP middleware for request authentication
//   - Deterministic UUID generation for user and tenant IDs
package authn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/env/config"
	httputil "github.com/pyck-ai/pyck/backend/common/http"
	"github.com/pyck-ai/pyck/backend/common/liveness"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/memkv"
	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
)

// ZitadelAuthProvider implements authentication using Zitadel as the identity provider.
// It provides token introspection with caching to reduce API calls and improve performance.
type ZitadelAuthProvider struct {
	systemTenantID uuid.UUID
	config         config.ZitadelConfig
	client         zitadel.Client
	tokenCache     *memkv.InMemoryKVStore
	// tenantSource answers which tenants are disabled. It is the same source
	// the tenant middleware uses, so both liveness checks read one table
	// through one key space and share every cached verdict.
	tenantSource liveness.TenantSource
	// livenessCache holds the tenant verdicts (liveness.TenantKey) this
	// provider reads and writes; it is shared with the tenant middleware and
	// the tenant-events subscriber and owned by main.
	livenessCache *liveness.Cache
	// livenessFaultLog keeps a failing source from writing one Error line per
	// request: faults are never cached, so during an outage every request
	// whose tenant has no cached verdict gets the fault back, and because the
	// source's dependency (management) is shared by the whole fleet, those
	// lines would otherwise arrive at the fleet's request rate.
	livenessFaultLog *log.FaultThrottle
	// noTenantClaimLog and noResourceOwnerLog throttle the two OIDC client
	// misconfiguration reports in introspect: a misconfigured client in a
	// retry loop would otherwise write one Error line per request, since a
	// refused token is never cached and an uncached one re-introspects.
	noTenantClaimLog   *log.FaultThrottle
	noResourceOwnerLog *log.FaultThrottle
}

// renamedLivenessCacheTTLEnv is the former name of PYCK_LIVENESS_CACHE_TTL.
const renamedLivenessCacheTTLEnv = "PYCK_ZITADEL_ORGANIZATION_CACHE_TTL"

// errPATCacheOverlap rejects a PAT cache overlap above the cache TTL, which
// would cache every token for the process lifetime.
var errPATCacheOverlap = errors.New("PYCK_ZITADEL_PAT_CACHE_TTL_OVERLAP must not exceed PYCK_ZITADEL_PAT_CACHE_TTL")

// errRenamedLivenessCacheTTL rejects an environment that still sets the
// former name of PYCK_LIVENESS_CACHE_TTL.
var errRenamedLivenessCacheTTL = errors.New(renamedLivenessCacheTTLEnv + " was renamed to PYCK_LIVENESS_CACHE_TTL")

// Ensure ZitadelAuthProvider implements AuthProvider interface
var _ Authenticator = (*ZitadelAuthProvider)(nil)

// NewZitadelAuthProvider creates a new Zitadel authentication provider.
//
// The [zitadel.Client] handles OIDC introspection. The
// [liveness.TenantSource] answers the liveness check that follows. The
// check runs on every Authenticate call (token-cache hit AND miss), but it
// only calls the source when the liveness cache holds no verdict for the
// tenant: a disabled tenant's tokens stop working once a tenant lifecycle
// event evicts that verdict or its TTL expires, rather than at the token's
// natural expiry, which introspection alone would wait for. Two sources ship
// with pyck: `managementapi.NewDisabledTenantsSource` for services that
// route through the federation gateway to management's `disabledTenants`
// query, and management's own inline closure reading its tenant table.
//
// This check is what covers the callers that authenticate without the tenant
// middleware (the NATS auth callout and the Temporal claim mapper) for
// which it is the only liveness gate on the path.
//
// The [liveness.Cache] holds the verdicts so the source does not run on
// every request; it is owned by the caller because the tenant middleware
// (acting-tenant verdicts) and the lifecycle-event subscriber (evictions)
// share it. This check covers the token owner's own tenant; the acting
// tenants of a request are checked by the tenant middleware against the same
// cache, under the same keys.
//
// Neither source nor cache may be nil, the PAT cache overlap must not
// exceed its TTL, and the renamed PYCK_ZITADEL_ORGANIZATION_CACHE_TTL must
// not be set. Each mistake is returned as an error so it fails at boot, not
// at the first authenticated request.
func NewZitadelAuthProvider(client zitadel.Client, cfg config.ZitadelConfig, tenantSource liveness.TenantSource, cache *liveness.Cache) (*ZitadelAuthProvider, error) {
	if tenantSource == nil || cache == nil {
		return nil, fmt.Errorf("zitadel auth provider: %w", liveness.ErrMissingDeps)
	}

	// Overlap must not exceed the TTL. When overlap > TTL every freshly
	// introspected token yields a negative effective TTL
	// (cacheExp.Sub(now) - overlap), which turns the cache into a
	// process-lifetime allowlist that outlives PAT/user revocation.
	// overlap == TTL is allowed: it yields a zero effective TTL, which
	// Authenticate treats as "do not cache, re-introspect every request",
	// a valid way to disable caching on purpose. Only the nonsensical
	// overlap > TTL is refused.
	if cfg.ZitadelPATCacheTTLOverlap > cfg.ZitadelPATCacheTTL {
		return nil, fmt.Errorf("%w: overlap %s, ttl %s", errPATCacheOverlap, cfg.ZitadelPATCacheTTLOverlap, cfg.ZitadelPATCacheTTL)
	}

	// PYCK_LIVENESS_CACHE_TTL replaced the org-verdict knob when the cache
	// widened to tenant verdicts. An environment that still sets the old
	// name clearly wants a non-default staleness bound; silently running
	// the default would undo that tuning.
	if os.Getenv(renamedLivenessCacheTTLEnv) != "" {
		return nil, errRenamedLivenessCacheTTL
	}

	return &ZitadelAuthProvider{
		systemTenantID:   ComputeUUID(cfg.ZitadelAudience, cfg.ZitadelOrganizationId),
		config:           cfg,
		client:           client,
		tokenCache:       memkv.NewInMemoryKVStore(cfg.ZitadelPATCacheTTL),
		tenantSource:     tenantSource,
		livenessCache:    cache,
		livenessFaultLog: log.NewFaultThrottle(log.FaultLogWindow),

		noTenantClaimLog:   log.NewFaultThrottle(log.FaultLogWindow),
		noResourceOwnerLog: log.NewFaultThrottle(log.FaultLogWindow),
	}, nil
}

// Close stops the cleanup goroutine of the token cache. Idempotent and safe
// to defer. Tests and any per-request provider accumulate goroutines
// without it. The liveness cache is owned and closed by the caller.
func (z *ZitadelAuthProvider) Close() {
	z.tokenCache.Close()
}

// OnTenantDisabled evicts every cached User entry whose home tenant
// matches the given ID. This is the sub-second fast path of tenant
// revocation for the token cache: the next request for any affected token
// misses the cache and runs the full chain (introspect + the liveness
// lookup). The tenant's liveness verdict is evicted separately by
// [liveness.Cache.EvictForTenantEvent]; [NewTenantEventHandler] combines
// both for the tenant-events subscriber.
//
// Zitadel introspection itself does NOT propagate org deactivation to
// its /introspect or /userinfo surface, so eviction alone is not
// sufficient: it must be paired with the liveness check, which runs on
// every request.
//
// Worst-case latency for tokens cached at the moment a NATS event is
// missed is bounded by LivenessCacheTTL, not ZitadelPATCacheTTL: the
// liveness check runs on token-cache hits as well as misses, so once the
// tenant's verdict expires the next request calls the source, which
// reports the disabled tenant.
func (z *ZitadelAuthProvider) OnTenantDisabled(tenantID uuid.UUID) {
	// O(k) eviction via the tenant secondary index — concurrent
	// Authenticate calls for unrelated tenants are not blocked by a
	// full-cache scan. Authenticate populates the index via
	// cache.SetWithSecondaryKey.
	z.tokenCache.DeleteBySecondaryKey(tenantID.String())
}

// Authenticate validates a token and returns the authenticated user information.
// It first checks the cache, then introspects the token with Zitadel if needed.
// The method handles multi-tenant scenarios where a user can have different roles
// in different organizations. When multiple roles exist for the same organization,
// the highest privilege role is retained.
func (z *ZitadelAuthProvider) Authenticate(ctx context.Context, token string) (User, error) {
	logger := log.ForContext(ctx)

	if token == "" {
		return User{}, ErrUnauthorized
	}

	// Cache hit short-circuits the Zitadel introspection (the slow
	// upstream call) but NOT the liveness check, which runs on every
	// request and calls the source only when no verdict is cached for
	// the tenant (see NewZitadelAuthProvider). A source fault also
	// 401s (an unverifiable tenant is not served) but only
	// a definite-no evicts the cached token: the fault is transient,
	// and evicting would re-introspect every request of the outage
	// (see rejectDisabledTenant for the rationale).
	if v, ok := z.tokenCache.Get(token); ok {
		if user, ok := v.(User); ok {
			if reject, definite := z.rejectDisabledTenant(ctx, logger, user, "cached token"); reject {
				if definite {
					z.tokenCache.Delete(token)
				}
				return User{}, ErrUnauthorized
			}
			return user, nil
		}
	}

	resp, err := z.client.IntrospectToken(ctx, token)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to introspect token")
		return User{}, ErrUnauthorized
	}

	if !resp.Active {
		logger.Error().Msg("Token is not active")
		return User{}, ErrUnauthorized
	}

	// Reject empty sub before caching: userID derives from it, so an
	// empty sub would mint one shared synthetic identity for every such
	// token and stamp it into created_by/updated_by. A response without a
	// sub is malformed; refuse it rather than cache it for the full TTL.
	if resp.Sub == "" {
		logger.Error().Msg("Token introspection returned empty sub")
		return User{}, ErrUnauthorized
	}

	// Generate deterministic UUIDs from issuer and IDs.
	userID := ComputeUUID(resp.Iss, resp.Sub)
	// Prefer the webhook-injected pyck_tenant_id claim — it's already
	// the canonical ComputeUUID(audience, orgID) value, parsed once.
	// Fall back to computing it locally when the claim is absent (PATs
	// minted before the webhook landed, system PAT, etc.).
	var tenantID uuid.UUID
	if resp.PyckTenantID != "" {
		parsed, perr := uuid.Parse(resp.PyckTenantID)
		if perr != nil {
			// Surface the misconfiguration; a silent fallback would hide
			// webhook/server disagreement about the claim format.
			logger.Warn().
				Err(perr).
				Str("pyck_tenant_id", resp.PyckTenantID).
				Msg("malformed pyck_tenant_id claim; ignoring it")
		} else {
			tenantID = parsed
		}
	}
	if tenantID == uuid.Nil {
		// With no usable claim and no resource owner the token names
		// neither a tenant nor an organization: every such token collapses
		// onto ComputeUUID(iss, ""), one synthetic ID shared by all of
		// them. The liveness axis would deny it (no row carries that ID)
		// but refusing here is earlier and says why. Both claims come from
		// the OIDC client's scopes, so this is a client misconfiguration,
		// and there is no tenant pyck could scope such a token to.
		if resp.ResourceOwnerID == "" {
			z.noTenantClaimLog.Err(logger, nil).
				Str("sub", resp.Sub).
				Msg("Token carries neither a resourceowner nor a usable pyck_tenant_id claim; nothing to scope it to; check the OIDC client's scopes")
			return User{}, ErrUnauthorized
		}
		tenantID = ComputeUUID(resp.Iss, resp.ResourceOwnerID)
	} else if resp.ResourceOwnerID != "" {
		// Claim and computed value disagree. Both are well-formed UUIDs,
		// so the cause is an audience/issuer drift between the webhook
		// and PYCK_ZITADEL_AUDIENCE here. Tripwire only — claim wins.
		// Only meaningful with a resource owner: without one there is
		// nothing to compute the local value from, so a mismatch would be
		// guaranteed and would say nothing about the configuration.
		if computed := ComputeUUID(resp.Iss, resp.ResourceOwnerID); computed != tenantID {
			logger.Warn().
				Str("claim", tenantID.String()).
				Str("computed", computed.String()).
				Msg("pyck_tenant_id claim differs from locally-computed value; check audience/issuer config")
		}
	}

	// A token with a tenant claim but no resource owner still names its
	// tenant through the claim, which is what checkTenantLive reads, so the
	// liveness axis covers it like any other token. Zitadel emits
	// the claim only when the token's scopes request it, so an empty value is
	// an OIDC client misconfiguration; report it here, once per introspection,
	// rather than on every request served from the token cache — the value is
	// frozen for the life of the token, so the verdict cannot change.
	if resp.ResourceOwnerID == "" {
		z.noResourceOwnerLog.Err(logger, nil).
			Str("sub", resp.Sub).
			Str("tenant_id", tenantID.String()).
			Msg("Token carries no resourceowner claim; the tenant liveness check runs on its pyck_tenant_id claim alone; check the OIDC client's scopes")
	}

	var roles map[uuid.UUID]Role
	var serviceRoles map[uuid.UUID]map[string]struct{}
	if resp.ProjectRoles != nil {
		roles = make(map[uuid.UUID]Role, len(resp.ProjectRoles))
		serviceRoles = make(map[uuid.UUID]map[string]struct{})

		for roleName, roleOrgMap := range resp.ProjectRoles {
			for orgID := range roleOrgMap {
				orgUUID := ComputeUUID(resp.Iss, orgID)

				// Per-service gate roles are recognised by suffix and tracked
				// separately from the privilege ladder; the gate (authn
				// middleware) enforces them per service. They are not part of
				// the reader/writer/admin hierarchy.
				if serviceroles.IsServiceRole(roleName) {
					if serviceRoles[orgUUID] == nil {
						serviceRoles[orgUUID] = make(map[string]struct{})
					}
					serviceRoles[orgUUID][roleName] = struct{}{}
					continue
				}

				role, err := RoleString(roleName)
				if err != nil {
					continue // Skip unknown roles
				}

				// Keep highest privilege role when multiple exist for same org
				if r, ok := roles[orgUUID]; !ok || r < role {
					roles[orgUUID] = role
				}
			}
		}
	}

	user := User{
		ID:           userID,
		Sub:          resp.Sub,
		Username:     resp.Username,
		TenantID:     tenantID,
		Roles:        roles,
		ServiceRoles: serviceRoles,
		Token:        token,
	}

	// Upgrade to system identity when the user holds ROLE_SYSTEM on
	// their home org. Anchor on the home-org-derived key so a
	// misconfigured pyck_tenant_id claim cannot silently demote a
	// system PAT to normal-user scoping.
	homeOrgTenantID := ComputeUUID(resp.Iss, resp.ResourceOwnerID)
	if user.HasRole(ROLE_SYSTEM, homeOrgTenantID) {
		// Tripwire, not a denial: a system-role token is expected to be
		// homed in the configured platform organization, and one homed
		// elsewhere is either a Zitadel role misconfiguration or a token
		// minted for another deployment. It is only logged because the one
		// known out-of-root system PAT candidate (pyck-admin-service,
		// provisioned by Terraform in another repository) cannot be
		// verified from this one, and locking the platform site out of
		// production on an unverified assumption is worse than logging.
		// Once every system PAT's home org is confirmed to be the root
		// org, enforcement is a one-line change: deny instead of log.
		if homeOrgTenantID != z.systemTenantID {
			logger.Error().
				Str("sub", resp.Sub).
				Str("home_tenant_id", homeOrgTenantID.String()).
				Str("system_tenant_id", z.systemTenantID.String()).
				Msg("system-role token homed outside the configured platform organization; upgrade still granted, flagged for enforcement")
		}
		user = *SystemUser()
	}

	log.ForContext(ctx).Debug().
		Bool("isSystemUser", user.IsSystemUser()).
		Str("systemTenant", z.systemTenantID.String()).
		Str("tenantID", tenantID.String()).
		Msg("auth")

	// Cache TTL is the minimum of token expiry and configured TTL,
	// minus overlap to ensure cached tokens are still valid
	now := time.Now().UTC()
	tokenExp := time.Unix(resp.Exp, 0)
	cacheExp := now.Add(z.config.ZitadelPATCacheTTL)

	if cacheExp.After(tokenExp) {
		cacheExp = tokenExp
	}

	ttl := cacheExp.Sub(now) - z.config.ZitadelPATCacheTTLOverlap

	// Tenant-validity gate. A DEFINITE rejection leaves the cache untouched
	// (we never Set), so a recovering tenant gets accepted as soon as the
	// org is re-activated. A FAULT rejection still caches the introspected
	// user below before refusing: the denial is transient, and without the
	// entry every retry of the outage (including freshly minted tokens
	// from clients re-logging-in) would re-introspect at Zitadel (per-call
	// key read, RSA sign, upstream round-trip) only to be refused again,
	// degrading the one identity dependency that is still healthy. Caching
	// it adds no staleness: the cache-hit path re-runs this probe on every
	// request, so the token stays refused until the source answers and is
	// served the moment it does.
	reject, definite := z.rejectDisabledTenant(ctx, logger, user, "post-introspection")
	if reject && definite {
		return User{}, ErrUnauthorized
	}

	// A non-positive effective TTL means there is no useful window to cache
	// for: the token sits inside the overlap, its own expiry is closer than
	// the overlap, or caching was disabled on purpose (overlap == TTL). This
	// is an accepted state, not an error — but the entry must NOT be written,
	// because memkv treats a zero TTL as "never expires", which is exactly
	// the eternal-allowlist defect from #1169. Skip the cache and
	// re-introspect on the next request. Logged at Debug, not Warn: under
	// overlap == TTL this fires on every request by design, and the only
	// genuinely broken config (overlap > TTL) is already rejected at boot.
	if ttl <= 0 {
		logger.Debug().
			Dur("ttl", ttl).
			Msg("non-positive PAT cache TTL; skipping cache and re-introspecting")
		if reject {
			return User{}, ErrUnauthorized
		}
		return user, nil
	}

	// Index by tenant ID so OnTenantDisabled evicts in O(k). System
	// users use the unindexed path — they're never the target of a
	// tenant-revocation event.
	if user.IsSystemUser() {
		z.tokenCache.Set(token, user, ttl)
	} else {
		z.tokenCache.SetWithSecondaryKey(token, user, ttl, user.TenantID.String())
	}

	if reject {
		return User{}, ErrUnauthorized
	}
	return user, nil
}

// checkTenantLive reports whether the tenant the user is homed in is still
// serving requests. System users (ROLE_SYSTEM machine accounts) bypass:
// they have no tenant scope and are validated implicitly by the system-role
// check during introspection.
//
// The verdict is the tenant row's: not soft-deleted, and its registration
// finished. It is cached under liveness.TenantKey(user.TenantID), the same
// key and the same source the tenant middleware uses for the acting set, so
// a request that passes here and then names its own tenant in the header
// costs one lookup, not two.
//
// A tenant ID that matches no row is denied, and management logs it. That is
// the source's contract (allow only what a live row backs) so a token
// naming a tenant this deployment does not have gets nothing, whether the
// tenant was never registered or its registration never completed. The
// platform's own identities are unaffected because they authenticate as
// system users, which return above. Nothing else is exempt: a zero tenant
// ID cannot reach here (Authenticate refuses a token that names neither a
// tenant nor an organization) and would be denied by the lookup anyway.
func (z *ZitadelAuthProvider) checkTenantLive(ctx context.Context, user User) (bool, error) {
	if user.IsSystemUser() {
		return true, nil
	}

	live, err := z.livenessCache.LookupTenants(ctx, []uuid.UUID{user.TenantID}, z.tenantSource)
	if err != nil {
		return false, err
	}
	return live[user.TenantID], nil
}

// rejectDisabledTenant runs the tenant liveness probe. reject reports that
// the caller must refuse the request; definite reports that the verdict came
// from the tenant row rather than a fault, and is what gates eviction of
// cached state.
//
// A definitive "tenant is not served" answer rejects (reject, definite). A
// validator infrastructure fault also rejects (a tenant that cannot be
// verified is not served, because letting it through would re-admit a
// deleted tenant for the length of a source outage) but NOT definitively
// (reject, !definite): the fault is transient, so cached tokens must stay
// cached. Evicting on a fault would send every request of the outage back
// through Zitadel introspection only to be refused again, and would delay
// recovery once the source answers. A cached live verdict keeps admitting
// requests through a fault for its TTL; after that the deny takes over
// until the source recovers.
//
// The fault is reported at Error once per [log.FaultLogWindow] and at Debug
// in between, carrying the suppressed count: the probe runs on every
// request, so an unthrottled Error line would be written once per request
// per replica for the length of the outage.
//
// phase is a free-form label that appears in the log message
// ("cached token", "post-introspection") so log readers know which
// auth-path stage rejected.
func (z *ZitadelAuthProvider) rejectDisabledTenant(ctx context.Context, logger *log.Logger, user User, phase string) (reject, definite bool) {
	live, err := z.checkTenantLive(ctx, user)
	if err != nil {
		z.livenessFaultLog.Err(logger, err).
			Str("tenant_id", user.TenantID.String()).
			Str("sub", user.Sub).
			Msgf("%s: tenant liveness lookup failed; denying unverified tenant", phase)
		return true, false
	}

	if !live {
		logger.Info().
			Str("tenant_id", user.TenantID.String()).
			Msgf("%s: tenant revoked", phase)
		return true, true
	}

	return false, false
}

// HTTPMiddleware returns an HTTP middleware that authenticates requests. If a
// Authentication header is present, it extracts the token and validates it.
// Returns 401 Unauthorized if authentication fails.
//
// The authenticated user can be retrieved using: auth.ForContext(r.Context())
func (z *ZitadelAuthProvider) HTTPMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// Extract token from Authorization header (supports "Bearer <token>" or raw token)
			token := r.Header.Get("Authorization")
			token = strings.TrimSpace(token)
			if strings.HasPrefix(token, "Bearer ") {
				token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer"))
			}

			if token != "" {
				auth, err := z.Authenticate(ctx, token)
				if err != nil {
					httputil.JSONError(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
					return
				}

				ctx = Context(ctx, &auth)
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
