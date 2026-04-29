package temporal

import (
	"context"
	"fmt"
	"time"

	"github.com/pyck-ai/pyck/backend/common/env"
	"github.com/pyck-ai/pyck/backend/common/guards"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/services/temporal"
)

// DefaultNamespace is the Temporal namespace used by platform workflows.
const DefaultNamespace = "default"

const (
	temporalTimeout       = 1 * time.Minute
	temporalRetryInterval = 5 * time.Second
)

// Run loads the Temporal configuration from the environment, waits for the
// server to become available, and creates the default namespace.
func Run(ctx context.Context) error {
	logger := log.ForContext(ctx)

	_, configuration, err := env.Load[Configuration](ctx)
	if err != nil {
		return fmt.Errorf("failed to load temporal bootstrap configuration: %w", err)
	}

	temporalURL := configuration.TemporalUrl

	logger.Debug().Str("url", temporalURL).Msg("Waiting for Temporal to become available")
	if err := waitForTemporal(ctx, temporalURL); err != nil {
		return fmt.Errorf("temporal dependency not ready: %w", err)
	}

	if err := bootstrap(ctx, temporalURL, DefaultNamespace); err != nil {
		return fmt.Errorf("failed to ensure temporal namespace: %w", err)
	}

	return nil
}

// waitForTemporal waits for the Temporal server to become reachable by
// attempting a health check via a short-lived client connection.
func waitForTemporal(ctx context.Context, temporalURL string) error {
	logger := log.ForContext(ctx)

	return guards.New().
		Add(guards.Check{
			Name:          "temporal",
			Timeout:       temporalTimeout,
			RetryInterval: temporalRetryInterval,
			CheckFunc: func(ctx context.Context) (bool, error) {
				logger.Debug().Str("url", temporalURL).Msg("Checking Temporal connectivity")
				c, err := temporal.NewTemporalClient(ctx, temporalURL)
				if err != nil {
					logger.Warn().Err(err).
						Dur("timeout", temporalTimeout).
						Dur("retry-interval", temporalRetryInterval).
						Msg("Temporal is not ready yet")
					return false, nil
				}
				defer c.Close()
				return true, nil
			},
		}).
		Wait(ctx)
}
