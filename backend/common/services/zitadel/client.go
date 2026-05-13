package zitadel

import (
	"context"

	"github.com/pyck-ai/pyck/backend/common/env/config"
)

type IntrospectionResult = IntrospectionResponse

type Client interface {
	IntrospectToken(ctx context.Context, token string) (*IntrospectionResult, error)
}

type client struct {
	httpClient *ZitadelHttpClient
}

func NewClient(config config.ZitadelConfig) *client {
	return &client{
		httpClient: HttpClient(config.ZitadelOAuthURL, config.ZitadelAudience, config.ZitadelAppKeyPath, !config.ZitadelTlsInsecure),
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
