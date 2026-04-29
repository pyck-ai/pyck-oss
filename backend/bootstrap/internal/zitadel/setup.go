package zitadel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pyck-ai/pyck/backend/common/env"
	"github.com/pyck-ai/pyck/backend/common/guards"
	"github.com/pyck-ai/pyck/backend/common/log"
)

const (
	zitadelTimeout       = 1 * time.Minute
	zitadelRetryInterval = 5 * time.Second
)

// Run loads the Zitadel configuration from the environment, waits for
// dependencies to become available, and bootstraps Zitadel with the provided
// initial data configuration.
func Run(ctx context.Context, zitadelConfig Zitadel) error {
	logger := log.ForContext(ctx)

	_, configuration, err := env.Load[Configuration](ctx)
	if err != nil {
		return fmt.Errorf("failed to load bootstrap configuration: %w", err)
	}

	logger.Debug().Msg("Waiting for Zitadel dependencies to become available")
	if err := waitForDependencies(ctx, configuration.KeyPath); err != nil {
		return fmt.Errorf("zitadel dependencies not ready: %w", err)
	}

	seeder, err := New(ctx, configuration)
	if err != nil {
		return fmt.Errorf("failed to initialize zitadel bootstrapper: %w", err)
	}

	if err := seeder.Bootstrap(ctx, zitadelConfig); err != nil {
		return fmt.Errorf("error during bootstrap zitadel: %w", err)
	}

	return nil
}

// waitForDependencies waits for the Zitadel admin key file to become available
// on the filesystem before proceeding with the bootstrap.
func waitForDependencies(ctx context.Context, keyPath string) error {
	var (
		logger       = log.ForContext(ctx)
		adminKeyFile = filepath.Join(keyPath, zitadelKeyFile)
	)

	return guards.New().
		Add(guards.Check{
			Name:          "zitadel-admin-key",
			Timeout:       zitadelTimeout,
			RetryInterval: zitadelRetryInterval,
			CheckFunc: func(ctx context.Context) (bool, error) {
				logger.Debug().Str("file", adminKeyFile).Msg("Waiting for Zitadel admin key to become available")
				_, err := os.Stat(adminKeyFile)
				if err == nil {
					return true, nil
				}
				if os.IsNotExist(err) {
					logger.Warn().
						Str("file", adminKeyFile).
						Dur("timeout", zitadelTimeout).
						Dur("retry-interval", zitadelRetryInterval).
						Msg("Zitadel admin key not available yet")
					return false, nil
				}
				return false, err
			},
		}).
		Wait(ctx)
}
