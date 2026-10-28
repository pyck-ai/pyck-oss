// Package dial holds the option builder for authenticated Zitadel gRPC
// connections. It lives apart from the parent services/zitadel package on
// purpose: importing zitadel-go's client packages registers proto
// extension 50001 on MethodOptions, which collides at init with the same
// number registered by go.temporal.io/server, and the parent package is
// (via common/authn) in the Temporal server's dependency graph. Only
// leaf consumers that actually dial Zitadel (the sdk, tenant
// registration) may import this package; never anything the Temporal
// server links.
package dial

import (
	"context"

	"github.com/zitadel/zitadel-go/v3/pkg/client/middleware"
	zitadelgo "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"

	zitadelsvc "github.com/pyck-ai/pyck/backend/common/services/zitadel"
)

// ConnectionOptions returns the option set for a Zitadel gRPC connection
// authenticated with the JWT-profile service account key at keyPath.
// insecure is the local stack: plaintext gRPC, and a token source that
// posts the JWT profile straight to the token endpoint at oauthURL because
// OIDC discovery refuses a plain-http issuer. A secure connection acquires
// its tokens through discovery instead.
func ConnectionOptions(ctx context.Context, oauthURL, audience, keyPath string, insecure bool) []zitadelgo.Option {
	if insecure {
		return []zitadelgo.Option{
			zitadelgo.WithInsecure(),
			zitadelgo.WithTokenSource(zitadelsvc.NewJWTProfileTokenSource(oauthURL, audience, keyPath)),
		}
	}
	return []zitadelgo.Option{
		zitadelgo.WithJWTProfileTokenSource(middleware.JWTProfileFromPath(ctx, keyPath)),
	}
}
