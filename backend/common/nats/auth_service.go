package nats

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/micro"
	"github.com/nats-io/nkeys"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/liveness"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/std"
)

const NatsAuthSubject = "$SYS.REQ.USER.AUTH"

// errCalloutBudgetBelowLookup rejects a configuration in which the liveness
// lookup cannot finish inside the callout it runs in. The callout's
// deadline would fire first, so every tenant without a cached verdict is
// refused at connect time while the lookup that would have answered is
// still running: a deployment-wide connect failure produced by two
// independently set environment variables, which is exactly the kind of
// pairing a process must refuse at startup rather than serve.
var errCalloutBudgetBelowLookup = errors.New("nats callout timeout must exceed the liveness lookup timeout")

// NewAuthService configures the NATS auth-callout micro service: every
// client connection is authenticated through auth and scoped to exactly one
// live tenant the user holds a role in (see authService.Handle). accountName
// is the NATS account the minted user JWT places the connection into: the
// tenant account, never the services' account; RequireSeparateTenantAccount
// checks that, and a caller runs it before this (management does at
// startup). The server matches it case-sensitively against its own config,
// and a mismatch rejects every callout-authenticated connection with an
// authorization violation. cache
// and source back the liveness axis and must not be nil; they are the same
// pair the HTTP middleware uses, so both read one verdict store.
//
// Refuses one configuration that breaks a relation no runtime error would
// report, because this is the one place that sees both sides of it: a
// callout budget that does not exceed the cache's lookup timeout. The
// callout's place inside the server's authorization timeout is pinned
// against the defaults by tests, not checked at startup; the user JWT TTL
// needs no check, because the expiry jitter scales down with it.
func NewAuthService(ctx context.Context, serviceName string, streamName string, accountName string, auth authn.Authenticator, keyPairSeed string, cache *liveness.Cache, source liveness.TenantSource, opts ...AuthOption) (micro.Config, error) {
	logger := log.ForContext(ctx)

	if cache == nil || source == nil {
		// The callout would otherwise grant tenant-scoped permissions with
		// the liveness axis silently absent.
		return micro.Config{}, fmt.Errorf("nats auth service: %w", liveness.ErrMissingDeps)
	}

	logger.Debug().
		Str("service", serviceName).
		Str("stream", streamName).
		Str("account", accountName).
		Str("subject", NatsAuthSubject).
		Msg("Initializing NATS auth service")

	description := fmt.Sprintf("%s Nats Auth Service", std.Title(serviceName))

	keyPair, err := nkeys.FromSeed([]byte(keyPairSeed))
	if err != nil {
		logger.Err(err).Msg("Failed to create key pair from seed")
		return micro.Config{}, err
	}

	calloutService := newAuthService(ctx, streamName, accountName, auth, keyPair, cache, source, opts...)

	if lookup := cache.LookupTimeout(); calloutService.calloutTimeout <= lookup {
		return micro.Config{}, fmt.Errorf("%w: callout %s, lookup %s",
			errCalloutBudgetBelowLookup, calloutService.calloutTimeout, lookup)
	}

	config := micro.Config{
		Name:        serviceName,
		Version:     "0.0.1",
		Description: description,
		Endpoint: &micro.EndpointConfig{
			Subject: NatsAuthSubject,
			Handler: calloutService,
		},
	}

	logger.Debug().
		Str("name", config.Name).
		Str("subject", config.Endpoint.Subject).
		Msg("Auth service configured")

	return config, nil
}
