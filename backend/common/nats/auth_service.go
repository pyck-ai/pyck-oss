package nats

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go/micro"
	"github.com/nats-io/nkeys"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/std"
)

const NatsAuthSubject = "$SYS.REQ.USER.AUTH"

func NewAuthService(ctx context.Context, serviceName string, streamName string, auth authn.Authenticator, keyPairSeed string) (micro.Config, error) {
	logger := log.ForContext(ctx)

	logger.Debug().
		Str("service", serviceName).
		Str("stream", streamName).
		Str("subject", NatsAuthSubject).
		Msg("Initializing NATS auth service")

	description := fmt.Sprintf("%s Nats Auth Service", std.Title(serviceName))

	keyPair, err := nkeys.FromSeed([]byte(keyPairSeed))
	if err != nil {
		logger.Err(err).Msg("Failed to create key pair from seed")
		return micro.Config{}, err
	}

	calloutService := newAuthService(ctx, streamName, auth, keyPair)

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
