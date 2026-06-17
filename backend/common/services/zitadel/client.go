package zitadel

import (
	"context"

	"github.com/pyck-ai/pyck/backend/common/env/config"
)

type IntrospectionResult = IntrospectionResponse

// Client is the introspection-side of the Zitadel HTTP surface. It does
// NOT carry the org-active check — that's a [authn.OrgValidator] passed
// alongside the client to [authn.NewZitadelAuthProvider]. Splitting them
// lets management swap in a local v2 SDK call while every other service
// queries management's `organization` federated GraphQL field through
// the gateway, without forcing the two responsibilities into the same
// interface.
type Client interface {
	IntrospectToken(ctx context.Context, token string) (*IntrospectionResult, error)
}

// OrganizationResult is the typed result returned by management's local
// ResolveOrganization helper and surfaced via the `organization`
// GraphQL resolver. Lives in common so non-management services can
// decode the same shape without a management import.
type OrganizationResult struct {
	Active         bool
	OrganizationID string
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
