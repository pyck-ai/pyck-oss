package authz

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/liveness"
	"github.com/pyck-ai/pyck/backend/common/log"
	"go.temporal.io/server/common/authorization"
)

// Authenticator is an interface for authentication providers that can authenticate users from tokens.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (authn.User, error)
}

// errClaimsTimeoutBelowLookup rejects a claims budget that does not exceed
// the liveness lookup timeout which runs inside it: introspection spends
// part of the budget first, so the lookup would be cut short by the
// mapping's deadline and every cold namespace would be denied.
var errClaimsTimeoutBelowLookup = errors.New("temporal claims timeout must exceed the liveness lookup timeout")

// NewClaimMapper creates a ClaimMapper with any AuthProvider implementation.
// This is useful for testing with mock auth providers.
//
// cache and source back the per-namespace tenant liveness filter (see
// GetClaims) and must not be nil; they are the same pair the auth provider
// uses for the home-tenant check, so both axes read one verdict store.
// claimsTimeout bounds one whole claim mapping (see GetClaims) and must
// exceed the cache's lookup timeout, which runs inside it. Either mistake
// is returned as an error so it fails at boot, not at the first
// authenticated request.
//
// claimsTimeout is positional because it is required and has no default
// here (the server config supplies it); NewAuthService takes its budgets
// as AuthOption values because each of them has a default. A further
// budget follows the same split: required → parameter, defaulted → option.
func NewClaimMapper(ctx context.Context, authProvider Authenticator, cache *liveness.Cache, source liveness.TenantSource, claimsTimeout time.Duration) (*ClaimMapper, error) {
	if cache == nil || source == nil {
		return nil, fmt.Errorf("temporal claim mapper: %w", liveness.ErrMissingDeps)
	}
	if lookup := cache.LookupTimeout(); claimsTimeout <= lookup {
		return nil, fmt.Errorf("%w: claims %s, lookup %s", errClaimsTimeoutBelowLookup, claimsTimeout, lookup)
	}
	return &ClaimMapper{
		authenticator: authProvider,
		contextBase:   func() context.Context { return ctx },
		livenessCache: cache,
		tenantSource:  source,
		claimsTimeout: claimsTimeout,
		faultLog:      log.NewFaultThrottle(log.FaultLogWindow),
	}, nil
}

type ClaimMapper struct {
	authenticator Authenticator
	contextBase   func() context.Context
	// livenessCache holds the tenant verdicts under liveness.TenantKey;
	// shared with the auth provider and the tenant-events subscriber.
	livenessCache *liveness.Cache
	// tenantSource answers cache misses (management's tenant table, through
	// the gateway).
	tenantSource liveness.TenantSource
	// claimsTimeout bounds one whole claim mapping (token introspection
	// and the tenant liveness lookup) from the top of GetClaims.
	//
	// Temporal hands the mapper no context of its own: the ClaimMapper
	// interface passes only *authorization.AuthInfo, so the work runs on
	// the process context, which nothing ever cancels. Without this bound
	// a dependency that accepts the connection and then stalls (the
	// classic load-balancer idle-drop) holds one frontend goroutine per
	// request, on the auth path of every gRPC call the public frontend
	// serves, until the process exits. The introspection client sets no
	// timeout of its own and the request it builds is context-aware, so
	// this deadline is the only thing that ends that call. The NATS auth
	// callout bounds its handler the same way and for the same reason.
	claimsTimeout time.Duration
	// faultLog keeps a failing source from writing one Error line per
	// request: the filter runs on every claim mapping, so an outage of the
	// shared source would otherwise emit at the request rate.
	faultLog *log.FaultThrottle
}

var (
	_ authorization.ClaimMapper                     = (*ClaimMapper)(nil)
	_ authorization.ClaimMapperWithAuthInfoRequired = (*ClaimMapper)(nil)
)

// AuthInfoRequired implements authorization.ClaimMapperWithAuthInfoRequired.
func (m *ClaimMapper) AuthInfoRequired() bool {
	return true
}

// GetClaims implements authorization.ClaimMapper.
func (m *ClaimMapper) GetClaims(authInfo *authorization.AuthInfo) (claims *authorization.Claims, err error) {
	ctx, cancel := context.WithTimeout(m.contextBase(), m.claimsTimeout)
	defer cancel()
	ext := &ClaimMapperExtensions{}

	claims = &authorization.Claims{
		Extensions: ext,
	}

	// Authenticate token (if provided)
	var token string

	if authInfo == nil || authInfo.AuthToken == "" {
		return claims, ErrNoAuthInfo
	}

	token = strings.TrimSpace(authInfo.AuthToken)
	token = strings.TrimPrefix(token, "Bearer ")
	token = strings.TrimSpace(token)

	ext.AuthInfo = authInfo

	user, err := m.authenticator.Authenticate(ctx, token)
	if err != nil {
		return claims, fmt.Errorf("%w: %w", ErrAuthFailed, err)
	}

	if !user.IsAuthenticated() {
		return claims, fmt.Errorf("%w: user is not authenticated", ErrAuthFailed)
	}

	ext.User = user

	// Map JWT claims to Temporal claims
	claims.Subject = user.ID.String()
	claims.Namespaces = m.namespaces(ctx, user)

	if user.IsSystemUser() {
		claims.System = authorization.RoleReader | authorization.RoleWriter
	} else {
		claims.System = authorization.RoleUndefined
	}

	return claims, nil
}

// namespaces maps the user's per-tenant roles to Temporal namespace roles,
// keeping only tenants a live row still backs: a stale Zitadel grant into a
// soft-deleted or never-registered tenant must not yield read/write access
// to that namespace's workflow histories and signals. The user's home
// tenant is already gated inside Authenticate; this filter covers the
// OTHER tenants of the roles map. System users bypass: their access does
// not derive from per-tenant grants, and the source is itself reached
// through a system token.
//
// A source error is an infrastructure fault, not a revocation; but a
// tenant that cannot be verified is not served: LookupTenants applies the
// cached verdicts (both signs keep working for their TTL) and reports the
// uncached tenants as not live, so during a fault an unverifiable tenant
// mints no namespace claim. Nothing is cached from the fault; claims
// return with the first successful lookup. Reported at Error once per
// log.FaultLogWindow and at Debug in between, carrying the suppressed
// count.
func (m *ClaimMapper) namespaces(ctx context.Context, user authn.User) map[string]authorization.Role {
	namespaces := make(map[string]authorization.Role, len(user.Roles))
	if user.IsSystemUser() {
		for tenant, role := range user.Roles {
			namespaces[tenant.String()] = TemporalRole(role)
		}
		return namespaces
	}

	live, err := m.livenessCache.LookupTenants(ctx, user.TenantIDs(), m.tenantSource)
	if err != nil {
		m.faultLog.Err(log.ForContext(ctx), err).
			Msg("disabled-tenant lookup failed; denying uncached tenants")
	}
	for tenant, role := range user.Roles {
		if !live[tenant] {
			continue
		}
		namespaces[tenant.String()] = TemporalRole(role)
	}
	return namespaces
}

func TemporalRole(role authn.Role) authorization.Role {
	switch role {
	case authn.ROLE_READER:
		return authorization.RoleReader
	case authn.ROLE_WRITER:
		return authorization.RoleReader | authorization.RoleWriter
	case authn.ROLE_ADMIN:
		return authorization.RoleReader | authorization.RoleWriter | authorization.RoleAdmin
	default:
		return authorization.RoleUndefined
	}
}

func PyckRole(role authorization.Role) authn.Role {
	switch {
	case role&authorization.RoleAdmin == authorization.RoleAdmin:
		return authn.ROLE_ADMIN
	case role&authorization.RoleWriter == authorization.RoleWriter:
		return authn.ROLE_WRITER
	case role&authorization.RoleReader == authorization.RoleReader:
		return authn.ROLE_READER
	default:
		return authn.ROLE_NONE
	}
}

type ClaimMapperExtensions struct {
	User     authn.User
	AuthInfo *authorization.AuthInfo
}
