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
		httpClient: HttpClient(config.ZitadelAudience, config.ZitadelAppKeyPath, !config.ZitadelTlsInsecure),
	}
}

func (c *client) IntrospectToken(ctx context.Context, token string) (*IntrospectionResult, error) {
	resp, err := c.httpClient.Introspect(token)
	if err != nil {
		return nil, err
	}

	return resp, nil
}
