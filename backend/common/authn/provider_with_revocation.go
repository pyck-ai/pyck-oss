package authn

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
)

// NewProviderWithRevocation bundles the auth-path wiring every non-
// management backend service needs: construct a [ZitadelAuthProvider]
// against the introspection client + org validator, and subscribe the
// provider's [ZitadelAuthProvider.OnTenantDisabled] callback to the
// tenant-revocation NATS topic so disabled tenants' cached entries
// are evicted within the JetStream-propagation window.
//
// Returns the provider, the consume cancel handle (the caller MUST
// `defer cc.Stop()`), and an error from the subscriber setup.
//
//nolint:ireturn // jetstream.ConsumeContext is the cancel handle the caller needs.
func NewProviderWithRevocation(
	ctx context.Context,
	client zitadel.Client,
	cfg config.ZitadelConfig,
	validator OrgValidator,
	js jetstream.JetStream,
	streamName, serviceName string,
) (*ZitadelAuthProvider, jetstream.ConsumeContext, error) {
	provider := NewZitadelAuthProvider(client, cfg, validator)
	cc, err := SubscribeRevocations(ctx, js, streamName, serviceName, provider.OnTenantDisabled)
	if err != nil {
		return nil, nil, fmt.Errorf("subscribe revocations: %w", err)
	}
	return provider, cc, nil
}
