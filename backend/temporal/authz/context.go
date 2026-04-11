package authz

import (
	"context"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"go.temporal.io/server/common/authorization"
)

func GetClaims(ctx context.Context) *authorization.Claims {
	claims, _ := ctx.Value(authorization.MappedClaims).(*authorization.Claims)
	return claims
}

func GetClaimExtensions(claims *authorization.Claims) *ClaimMapperExtensions {
	if claims == nil {
		return nil
	}

	ext, _ := claims.Extensions.(*ClaimMapperExtensions)
	return ext
}

func GetUser(claims *authorization.Claims) authn.User {
	ext := GetClaimExtensions(claims)
	if ext == nil {
		return authn.User{}
	}

	return ext.User
}
