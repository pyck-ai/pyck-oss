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
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/env/config"
	httputil "github.com/pyck-ai/pyck/backend/common/http"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/memkv"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
)

// ZitadelAuthProvider implements authentication using Zitadel as the identity provider.
// It provides token introspection with caching to reduce API calls and improve performance.
type ZitadelAuthProvider struct {
	systemTenantID uuid.UUID
	config         config.ZitadelConfig
	client         zitadel.Client
	cache          *memkv.InMemoryKVStore
	orgValidator   OrgValidator
}

// OrgValidator is the post-introspection check: "is the org behind
// this `sub` still active?". `(true, nil)` proceeds; `(false, nil)`
// is a routine revoke (401 + cache eviction); `(false, err)` is an
// infra fault (logged at Error, NOT treated as a revocation — the
// cache TTL bounds the staleness). See package docs for the two
// implementations that ship.
type OrgValidator func(ctx context.Context, sub string) (active bool, err error)

// Ensure ZitadelAuthProvider implements AuthProvider interface
var _ Authenticator = (*ZitadelAuthProvider)(nil)

// NewZitadelAuthProvider creates a new Zitadel authentication provider.
//
// The [zitadel.Client] handles OIDC introspection. The [OrgValidator]
// handles the org-active check that follows: it runs on every
// Authenticate call (cache hit AND miss) so a token whose owning org
// gets deactivated stops working on the very next request. Two
// validators ship with pyck — `managementapi.NewOrganizationValidator`
// for services that route through the federation gateway to
// management's `organization` GraphQL query, and management's own
// inline closure that calls its local v2 SDK helper directly against
// the system gRPC connection.
//
// orgValidator MUST NOT be nil; without it a revoked tenant's tokens
// stay accepted until their natural expiry because Zitadel
// introspection does not propagate org deactivation.
func NewZitadelAuthProvider(client zitadel.Client, config config.ZitadelConfig, orgValidator OrgValidator) *ZitadelAuthProvider {
	return &ZitadelAuthProvider{
		systemTenantID: ComputeUUID(config.ZitadelAudience, config.ZitadelOrganizationId),
		config:         config,
		client:         client,
		cache:          memkv.NewInMemoryKVStore(config.ZitadelPATCacheTTL),
		orgValidator:   orgValidator,
	}
}

// OnTenantDisabled evicts every cached User entry whose tenant matches the
// given ID. This is the sub-second fast path of tenant revocation: the
// next request for any affected token misses the cache and runs the full
// chain (introspect + [OrgValidator]); the validator then catches the
// disabled tenant and the lookup is NOT re-cached.
//
// Zitadel introspection itself does NOT propagate org deactivation to
// its /introspect or /userinfo surface, so eviction alone is not
// sufficient — it must be paired with the validator on the cache-miss
// path. The combination guarantees that revocation takes effect within
// either the NATS-propagation window (~ms) for already-cached tokens, or
// the next request for previously-uncached tokens.
//
// Intended to be wired as the callback for [SubscribeRevocations], which
// reads management's tenant CRUD topic and fires on soft-delete transitions.
//
// Worst-case latency for tokens cached at the moment a NATS event is
// missed is bounded by ZitadelPATCacheTTL: stale entries auto-expire,
// then the validator catches the disabled tenant on the next miss.
func (z *ZitadelAuthProvider) OnTenantDisabled(tenantID uuid.UUID) {
	z.cache.DeleteWhere(func(_ string, v any) bool {
		u, ok := v.(User)
		return ok && u.TenantID == tenantID
	})
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
	// upstream call) but NOT the tenant validator — the validator
	// runs on every request so a deactivated org rejects tokens
	// within the next request, not after the cache TTL. Only a
	// definite-no from the validator evicts and 401s; transport /
	// upstream faults stay with the cached decision (see
	// rejectOrgActive for the rationale).
	if v, ok := z.cache.Get(token); ok {
		if user, ok := v.(User); ok {
			if z.rejectOrgActive(ctx, logger, user, "cached token") {
				z.cache.Delete(token)
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
				Msg("malformed pyck_tenant_id claim; falling back to computed tenant ID")
		} else {
			tenantID = parsed
		}
	}
	if tenantID == uuid.Nil {
		tenantID = ComputeUUID(resp.Iss, resp.ResourceOwnerID)
	} else if computed := ComputeUUID(resp.Iss, resp.ResourceOwnerID); computed != tenantID {
		// Claim and computed value disagree. Both are well-formed UUIDs,
		// so the cause is an audience/issuer drift between the webhook
		// and PYCK_ZITADEL_AUDIENCE here. Tripwire only — claim wins.
		logger.Warn().
			Str("claim", tenantID.String()).
			Str("computed", computed.String()).
			Msg("pyck_tenant_id claim differs from locally-computed value; check audience/issuer config")
	}

	var roles map[uuid.UUID]Role
	if resp.ProjectRoles != nil {
		roles = make(map[uuid.UUID]Role, len(resp.ProjectRoles))

		for roleName, roleOrgMap := range resp.ProjectRoles {
			for orgID := range roleOrgMap {
				role, err := RoleString(roleName)
				if err != nil {
					continue // Skip unknown roles
				}

				orgUUID := ComputeUUID(resp.Iss, orgID)

				// Keep highest privilege role when multiple exist for same org
				if r, ok := roles[orgUUID]; !ok || r < role {
					roles[orgUUID] = role
				}
			}
		}
	}

	user := User{
		ID:       userID,
		Sub:      resp.Sub,
		Username: resp.Username,
		TenantID: tenantID,
		Roles:    roles,
		Token:    token,
	}

	// If the user has ROLE_SYSTEM on their home org, overwrite with the
	// system service-user identity so downstream code skips all tenant
	// scoping.
	if user.HasRole(ROLE_SYSTEM, tenantID) {
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

	// Tenant-validity gate. Validator failure on this fresh introspect
	// path leaves the cache untouched (we never Set), so a recovering
	// tenant gets accepted as soon as the org is re-activated.
	if z.rejectOrgActive(ctx, logger, user, "post-introspection") {
		return User{}, ErrUnauthorized
	}

	z.cache.Set(token, user, ttl)

	return user, nil
}

// checkOrgActive delegates to the configured [OrgValidator] with the
// user's raw Zitadel `sub`. System users (ROLE_SYSTEM machine
// accounts) bypass — they have no tenant scope and are validated
// implicitly by the system-role check during introspection.
func (z *ZitadelAuthProvider) checkOrgActive(ctx context.Context, user User) (bool, error) {
	if user.IsSystemUser() {
		return true, nil
	}
	return z.orgValidator(ctx, user.Sub)
}

// rejectOrgActive runs the org-active probe and returns true when the
// caller should reject (eviction + 401).
//
// Only a definitive `(false, nil)` "org is inactive" answer rejects.
// A validator infrastructure fault (`(false, err)`) is NOT a
// revocation — we don't know — so we log at Error for ops visibility
// and accept the same staleness window the cache TTL already accepts.
// Treating err as revoke would amplify any blip in the validator's
// transport chain into fleet-wide 401s + a load spike against the
// degraded dependency (every evicted token re-introspects and fails
// the validator again).
//
// phase is a free-form label that appears in the log message
// ("cached token", "post-introspection") so log readers know which
// auth-path stage rejected.
func (z *ZitadelAuthProvider) rejectOrgActive(ctx context.Context, logger *log.Logger, user User, phase string) bool {
	active, err := z.checkOrgActive(ctx, user)
	if err != nil {
		// Infrastructure fault, not a revocation. Stay with the cached
		// decision; let the cache TTL bound the staleness.
		logger.Error().Err(err).
			Str("tenant_id", user.TenantID.String()).
			Msgf("%s: org-validator failed; not treating as revocation", phase)
		return false
	}

	if !active {
		logger.Info().
			Str("tenant_id", user.TenantID.String()).
			Msgf("%s: tenant revoked", phase)
		return true
	}

	return false
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
