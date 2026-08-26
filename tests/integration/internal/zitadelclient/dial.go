// Package zitadelclient wraps the upstream Zitadel SDK with the bits
// integration suites need: a connection dialed via the admin SA JWT
// profile, machine-user provisioning, and project grants.
package zitadelclient

import (
	"context"
	"fmt"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/zitadel-go/v3/pkg/client/middleware"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"

	"github.com/pyck-ai/pyck/tests/integration/internal/config"
)

// Dial opens a single gRPC connection to Zitadel using the admin SA JWT
// profile flow. Caller owns Close.
//
// `ScopeZitadelAPI` is the standard scope for the Zitadel system project
// itself; combined with openid it gets us a token that the user/authz
// gRPC services accept.
func Dial(ctx context.Context, cfg *config.Config) (*zitadel.Connection, error) {
	conn, err := zitadel.NewConnection(
		ctx,
		cfg.ZitadelIssuer,
		cfg.ZitadelGrpcAddr,
		[]string{oidc.ScopeOpenID, zitadel.ScopeZitadelAPI()},
		zitadel.WithJWTProfileTokenSource(middleware.JWTProfileFromPath(ctx, cfg.ZitadelKeyfile)),
		zitadel.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("dial zitadel: %w", err)
	}
	return conn, nil
}
