package events

import (
	"context"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/pyck-ai/pyck/backend/common/log"
)

func NewNatsClient(ctx context.Context, natsURL string) (*nats.Conn, error) {
	client, err := nats.Connect(
		natsURL,
		nats.RetryOnFailedConnect(true),
		nats.ConnectHandler(connectHandler(ctx)),
		nats.DisconnectErrHandler(disconnectErrorHandler(ctx)),
		nats.ReconnectHandler(reconnectHandler(ctx)),
		nats.ClosedHandler(closedHandler(ctx)),
	)
	if err != nil {
		return nil, err
	}

	return client, nil
}

func CreateOrUpdateJetstream(ctx context.Context, natsClient *nats.Conn, streamName string, natsReplicaNo int) (jetstream.JetStream, error) {
	js, err := jetstream.New(natsClient)
	if err != nil {
		return nil, err
	}

	streamConfig := jetstream.StreamConfig{
		Name:         streamName,
		Subjects:     []string{streamName + ".>"},
		Replicas:     natsReplicaNo,
		MaxConsumers: 200,
		MaxAge:       time.Hour * 24 * 3,
		Retention:    jetstream.LimitsPolicy,
		Discard:      jetstream.DiscardOld,
		ConsumerLimits: jetstream.StreamConsumerLimits{
			InactiveThreshold: time.Hour * 24 * 3, // Consumers inactive for 3 days may be removed
			MaxAckPending:     10000,              // Maximum number of messages without acknowledgement
		},
	}
	_, err = js.CreateOrUpdateStream(ctx, streamConfig)
	if err != nil {
		return nil, err
	}
	return js, nil
}

func CreateOrUpdateStream(ctx context.Context, js jetstream.JetStream, streamName string, natsReplicaNo int, subjects []string) (jetstream.JetStream, error) {
	streamConfig := jetstream.StreamConfig{
		Name:         streamName,
		Subjects:     subjects,
		Replicas:     natsReplicaNo,
		MaxConsumers: 200,
		MaxAge:       time.Hour * 24 * 3,
		Retention:    jetstream.LimitsPolicy,
		Discard:      jetstream.DiscardOld,
	}

	_, err := js.CreateOrUpdateStream(ctx, streamConfig)
	if err != nil {
		return nil, err
	}
	return js, nil
}

func connectHandler(ctx context.Context) nats.ConnHandler {
	logger := log.ForContext(ctx).With().
		Str("component", "nats-client").
		Logger()
	return func(_ *nats.Conn) {
		logger.Info().Msg("NATS connected")
	}
}

func disconnectErrorHandler(ctx context.Context) nats.ConnErrHandler {
	logger := log.ForContext(ctx).With().
		Str("component", "nats-client").
		Logger()
	return func(_ *nats.Conn, err error) {
		logger.Warn().Err(err).Msg("NATS disconnected")
	}
}

func reconnectHandler(ctx context.Context) nats.ConnHandler {
	logger := log.ForContext(ctx).With().
		Str("component", "nats-client").
		Logger()
	return func(_ *nats.Conn) {
		logger.Info().Msg("NATS reconnected")
	}
}

func closedHandler(ctx context.Context) nats.ConnHandler {
	logger := log.ForContext(ctx).With().
		Str("component", "nats-client").
		Logger()
	return func(_ *nats.Conn) {
		logger.Info().Msg("NATS connection closed")
	}
}
