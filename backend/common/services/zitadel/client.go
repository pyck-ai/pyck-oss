package zitadel

import (
	"context"

	"github.com/pyck-ai/pyck/backend/common/env/config"
)

type IntrospectionResult = IntrospectionResponse

// Client is the introspection-side of the Zitadel HTTP surface. It does
// NOT carry the tenant liveness check: that's a [liveness.TenantSource]
// passed alongside the client to [authn.NewZitadelAuthProvider]. Splitting
// them lets management read its own tenant table in-process while every
// other service queries management's `disabledTenants` federated GraphQL
// field through the gateway, without forcing the two responsibilities into
// the same interface.
type Client interface {
	IntrospectToken(ctx context.Context, token string) (*IntrospectionResult, error)
}

type client struct {
	httpClient *ZitadelHttpClient
}

// NewClient returns the introspection-only Zitadel client.
func NewClient(cfg config.ZitadelConfig) *client {
	return &client{
		httpClient: HttpClient(cfg.ZitadelOAuthURL, cfg.ZitadelAudience, cfg.ZitadelAppKeyPath, !cfg.ZitadelTlsInsecure),
	}
}

// TODO(jan) The zitadel-go SDK provides OIDC middleware that handles introspection automatically via rs.WithIntrospection(). It
// would replace the manual HTTP POST to /oauth/v2/introspect with JWT client assertion that the current http_client.go does by hand.
func (c *client) IntrospectToken(ctx context.Context, token string) (*IntrospectionResult, error) {
	resp, err := c.httpClient.Introspect(ctx, token)
	if err != nil {
		return nil, err
	}

	return resp, nil
}
