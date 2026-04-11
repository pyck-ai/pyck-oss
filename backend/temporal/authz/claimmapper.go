package authz

import (
	"context"
	"fmt"
	"strings"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"go.temporal.io/server/common/authorization"
)

// Authenticator is an interface for authentication providers that can authenticate users from tokens.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (authn.User, error)
}

// NewClaimMapper creates a ClaimMapper with any AuthProvider implementation.
// This is useful for testing with mock auth providers.
func NewClaimMapper(ctx context.Context, authProvider Authenticator) *ClaimMapper {
	return &ClaimMapper{
		authenticator: authProvider,
		contextBase:   func() context.Context { return ctx },
	}
}

type ClaimMapper struct {
	authenticator Authenticator
	contextBase   func() context.Context
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
	var (
		ctx = m.contextBase()
		ext = &ClaimMapperExtensions{}
	)

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
	claims.Namespaces = make(map[string]authorization.Role, len(user.Roles))

	for tenant, role := range user.Roles {
		claims.Namespaces[tenant.String()] = TemporalRole(role)
	}

	if user.IsSystemUser() {
		claims.System = authorization.RoleReader | authorization.RoleWriter
	} else {
		claims.System = authorization.RoleUndefined
	}

	return claims, nil
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
