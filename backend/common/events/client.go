package events

import (
	"context"
	"errors"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/pyck-ai/pyck/backend/common/log"
)

// DrainTimeout bounds NATS drains during shutdown — both the connection
// drain (DrainNatsClient) and per-consumer drains that precede it. It only
// has to cover ACK round-trips for messages already in flight; everything
// publishing has stopped by then.
const DrainTimeout = 5 * time.Second

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

// DrainNatsClient drains the connection — completing delivery and ACKs of
// in-flight messages across all subscriptions — and blocks until the drain
// finishes or timeout elapses, then falls back to a hard Close. Use as the
// final NATS teardown step, after everything publishing to the connection
// (outbox, event system) has stopped.
func DrainNatsClient(ctx context.Context, nc *nats.Conn, timeout time.Duration) {
	logger := log.ForContext(ctx).With().
		Str("component", "nats-client").
		Logger()

	if nc.IsClosed() {
		return
	}

	// Replaces the logging handler installed by NewNatsClient, so keep its
	// log line: this is the only close notification during shutdown.
	closed := make(chan struct{})
	nc.SetClosedHandler(func(_ *nats.Conn) {
		logger.Info().Msg("NATS connection closed")
		close(closed)
	})

	if err := nc.Drain(); err != nil {
		// A connection still handshaking cannot be drained, and Drain has
		// already closed it. That is the expected outcome when a startup
		// abort tears down a connection dialled against a NATS that never
		// came up — RetryOnFailedConnect means NewNatsClient returns while
		// still connecting — so it is not worth a warning.
		if errors.Is(err, nats.ErrConnectionReconnecting) {
			logger.Debug().Err(err).Msg("NATS connection not established, closing")
		} else {
			logger.Warn().Err(err).Msg("NATS drain failed, closing")
		}
		nc.Close()
		return
	}

	select {
	case <-closed:
		logger.Info().Msg("NATS connection drained")
	case <-time.After(timeout):
		logger.Warn().Dur("timeout", timeout).Msg("NATS drain timed out, closing")
		nc.Close()
	}
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
