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
}

// Ensure ZitadelAuthProvider implements AuthProvider interface
var _ Authenticator = (*ZitadelAuthProvider)(nil)

// NewZitadelAuthProvider creates a new Zitadel authentication provider
// with the given configuration and client.
func NewZitadelAuthProvider(client zitadel.Client, config config.ZitadelConfig) *ZitadelAuthProvider {
	return &ZitadelAuthProvider{
		systemTenantID: ComputeUUID(config.ZitadelAudience, config.ZitadelOrganizationId),
		config:         config,
		client:         client,
		cache:          memkv.NewInMemoryKVStore(config.ZitadelPATCacheTTL),
	}
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

	// Check cache first
	if v, ok := z.cache.Get(token); ok {
		if user, ok := v.(User); ok {
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

	// Generate deterministic UUIDs from issuer and IDs
	userID := ComputeUUID(resp.Iss, resp.Sub)
	tenantID := ComputeUUID(resp.Iss, resp.ResourceOwnerID)

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
		Username: resp.Username,
		TenantID: tenantID,
		Roles:    roles,
		Token:    token,
	}

	// If the user is the "system" service user, overwrite the token permissions
	if user.HasRole(ROLE_SYSTEM, ComputeUUID(resp.Iss, resp.ResourceOwnerID)) {
		user = *SystemUser()
	}

	log.ForContext(ctx).Debug().
		Bool("isSystemUser", user.IsSystemUser()).
		Str("systemTenant", z.systemTenantID.String()).
		Str("ownerTenant", ComputeUUID(resp.Iss, resp.ResourceOwnerID).String()).
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

	z.cache.Set(token, user, ttl)

	return user, nil
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
