package authn

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/liveness"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
)

// NewTenantEventHandler builds the standard [TenantEventFunc]: it evicts the
// tenant's liveness verdict and, on disable, drops the provider's cached
// tokens of users homed in that tenant so their next request re-runs the
// full chain.
func NewTenantEventHandler(cache *liveness.Cache, provider *ZitadelAuthProvider) TenantEventFunc {
	return func(ev TenantEvent) {
		cache.EvictForTenantEvent(ev.TenantID)
		if ev.Disabling {
			provider.OnTenantDisabled(ev.TenantID)
		}
	}
}

// NewProviderWithTenantEvents bundles the auth-path wiring every backend
// service needs: construct a [ZitadelAuthProvider] against the
// introspection client, the tenant-liveness source and the shared liveness
// cache, and subscribe [NewTenantEventHandler] to the tenant CRUD NATS
// topic so cached verdicts follow tenant disable/restore within the
// JetStream-propagation window.
//
// Returns the provider (the caller owns it and may `defer provider.Close()`),
// the consume cancel handle (the caller MUST `defer cc.Stop()`), and an
// error from the provider or the subscriber setup. On error nothing is
// left running: a provider built before the subscribe failed is closed
// here, since the caller never receives it and its token-cache janitor
// goroutine would otherwise outlive the failed setup.
//
//nolint:ireturn // jetstream.ConsumeContext is the cancel handle the caller needs.
func NewProviderWithTenantEvents(
	ctx context.Context,
	client zitadel.Client,
	cfg config.ZitadelConfig,
	tenantSource liveness.TenantSource,
	cache *liveness.Cache,
	js jetstream.JetStream,
	streamName, serviceName string,
) (*ZitadelAuthProvider, jetstream.ConsumeContext, error) {
	provider, err := NewZitadelAuthProvider(client, cfg, tenantSource, cache)
	if err != nil {
		return nil, nil, err
	}
	cc, err := SubscribeTenantEvents(ctx, js, streamName, serviceName, NewTenantEventHandler(cache, provider))
	if err != nil {
		provider.Close()
		return nil, nil, fmt.Errorf("subscribe revocations: %w", err)
	}
	return provider, cc, nil
}
