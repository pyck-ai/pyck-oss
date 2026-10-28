package events

import (
	"context"
	"errors"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/pyck-ai/pyck/backend/common/log"
)

const (
	// DrainTimeout bounds NATS drains during shutdown — both the connection
	// drain (DrainNatsClient) and per-consumer drains that precede it. It only
	// has to cover ACK round-trips for messages already in flight; everything
	// publishing has stopped by then.
	DrainTimeout = 5 * time.Second

	// StreamDuplicateWindow is how long the tenant and system streams remember
	// a Nats-Msg-Id. It equals the NATS default; set explicitly because
	// state-change publishing relies on it.
	StreamDuplicateWindow = 2 * time.Minute
)

func NewNatsClient(ctx context.Context, natsURL string) (*nats.Conn, error) {
	client, err := nats.Connect(
		natsURL,
		nats.RetryOnFailedConnect(true),
		// The platform must ride out auth refusals rather than give up:
		// the auth callout denies while its dependencies (Zitadel, the
		// tenant liveness source) are unreachable, and the client's
		// default behaviour (abort reconnecting after the same auth
		// error twice, cap reconnects at 60) would turn such a blip
		// into a permanently closed connection that silently stops the
		// outbox drainer and every consumer until the pod restarts.
		// Reconnect forever and treat auth errors as retryable; a
		// genuinely revoked credential then shows as an endless logged
		// reconnect loop, which monitoring catches, instead of a dead
		// pod that looks healthy.
		nats.IgnoreAuthErrorAbort(),
		nats.MaxReconnects(-1),
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
