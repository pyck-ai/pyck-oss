package minio

import (
	"context"
	"fmt"
	"time"

	"github.com/pyck-ai/pyck/backend/common/env"
	"github.com/pyck-ai/pyck/backend/common/guards"
	"github.com/pyck-ai/pyck/backend/common/log"
)

const (
	minioTimeout       = 2 * time.Minute
	minioRetryInterval = 5 * time.Second
)

// Run loads the MinIO configuration from the environment, waits for MinIO
// to become available, and ensures the configured bucket exists.
func Run(ctx context.Context) error {
	logger := log.ForContext(ctx)

	_, configuration, err := env.Load[Configuration](ctx)
	if err != nil {
		return fmt.Errorf("failed to load minio bootstrap configuration: %w", err)
	}

	logger.Debug().
		Str("endpoint", configuration.Endpoint).
		Str("bucket", configuration.Bucket).
		Msg("Starting MinIO bootstrap")

	// Wait for MinIO to become available
	if err := waitForMinIO(ctx, &configuration); err != nil {
		return fmt.Errorf("minio dependency not ready: %w", err)
	}

	// Ensure bucket exists
	if err := bootstrap(ctx, &configuration); err != nil {
		return fmt.Errorf("failed to bootstrap minio: %w", err)
	}

	logger.Debug().
		Str("bucket", configuration.Bucket).
		Msg("MinIO bootstrap completed successfully")

	return nil
}

// waitForMinIO waits for the MinIO server to become reachable by attempting
// to connect and list buckets.
func waitForMinIO(ctx context.Context, config *Configuration) error {
	logger := log.ForContext(ctx)

	return guards.New().
		Add(guards.Check{
			Name:          "minio",
			Timeout:       minioTimeout,
			RetryInterval: minioRetryInterval,
			CheckFunc: func(ctx context.Context) (bool, error) {
				logger.Debug().
					Str("endpoint", config.Endpoint).
					Msg("Checking MinIO connectivity")

				client, err := newMinIOClient(config)
				if err != nil {
					logger.Warn().Err(err).
						Dur("timeout", minioTimeout).
						Dur("retry-interval", minioRetryInterval).
						Msg("MinIO is not ready yet")
					return false, nil
				}

				// Try to list buckets to verify connection
				_, err = client.ListBuckets(ctx)
				if err != nil {
					logger.Warn().Err(err).
						Dur("timeout", minioTimeout).
						Dur("retry-interval", minioRetryInterval).
						Msg("MinIO connection check failed")
					return false, nil
				}

				logger.Debug().Msg("MinIO is ready")
				return true, nil
			},
		}).
		Wait(ctx)
}
