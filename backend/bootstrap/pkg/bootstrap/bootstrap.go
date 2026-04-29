package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	_ "embed"

	_ "github.com/lib/pq"
	"gopkg.in/yaml.v2"

	"github.com/pyck-ai/pyck/backend/bootstrap/internal/database"
	"github.com/pyck-ai/pyck/backend/bootstrap/internal/minio"
	"github.com/pyck-ai/pyck/backend/bootstrap/internal/temporal"
	"github.com/pyck-ai/pyck/backend/bootstrap/internal/zitadel"
	"github.com/pyck-ai/pyck/backend/common/env"
	envconfig "github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/log"
)

const (
	// defaultTimeout is the default timeout for the entire bootstrap process.
	defaultTimeout = 5 * time.Minute
)

// bootstrapFunc is the signature for a bootstrap function.
type bootstrapFunc func(context.Context, Options) error

var (
	// DefaultBootstrapConfig is the embedded default bootstrap configuration.
	//
	//go:embed bootstrap.yaml
	DefaultBootstrapConfig []byte

	// bootstrappers maps each module to its bootstrap function.
	bootstrappers = map[BootstrapModule]bootstrapFunc{
		BootstrapModuleZitadel:  bootstrapZitadel,
		BootstrapModuleTemporal: bootstrapTemporal,
		BootstrapModuleMinio:    bootstrapMinIO,
	}
)

// Bootstrap initializes an external dependency (Zitadel, Temporal, or MinIO)
// before the management service starts.
func Bootstrap(ctx context.Context, dbConfig envconfig.DbConfig, module BootstrapModule) error {
	opts := Options{
		ServiceName:   "bootstrap-" + module.String(),
		DbMasterUrl:   dbConfig.DbMasterUrl,
		DbDriver:      dbConfig.DbDriver,
		DefaultConfig: DefaultBootstrapConfig,
	}

	f, ok := bootstrappers[module]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownBootstrapModule, module)
	}

	return f(ctx, opts)
}

// bootstrapZitadel initializes Zitadel before a
// service starts. It loads configuration, acquires a distributed lock, and
// runs the Zitadel bootstrap step.
func bootstrapZitadel(ctx context.Context, opts Options) (err error) {
	logger := log.ForContext(ctx)

	_, configuration, err := env.Load[Configuration](ctx)
	if err != nil {
		return fmt.Errorf("failed to load bootstrap configuration: %w", err)
	}

	bootstrapConfig, err := loadConfig(ctx, configuration.ConfigFile, opts.DefaultConfig)
	if err != nil {
		return err
	}

	timeout := configuration.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	bootstrapCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dbClient, err := sql.Open(opts.DbDriver, opts.DbMasterUrl)
	if err != nil {
		return fmt.Errorf("failed to open database connection: %w", err)
	}
	defer dbClient.Close()

	logger.Debug().Msg("Waiting for lock")
	unlock, err := database.AcquireLock(bootstrapCtx, dbClient, opts.ServiceName, opts.DbDriver)
	if err != nil {
		return err
	}

	defer func() {
		logger.Debug().Msg("Unlocking")
		if uerr := unlock(); uerr != nil {
			err = errors.Join(err, fmt.Errorf("release lock: %w", uerr))
		}
	}()

	logger.Info().Msg("Bootstrapping Zitadel")
	if err := zitadel.Run(bootstrapCtx, bootstrapConfig.Zitadel); err != nil {
		return fmt.Errorf("cannot bootstrap Zitadel: %w", err)
	}

	return nil
}

// bootstrapTemporal initializes Temporal
func bootstrapTemporal(ctx context.Context, opts Options) (err error) {
	logger := log.ForContext(ctx)

	_, configuration, err := env.Load[Configuration](ctx)
	if err != nil {
		return fmt.Errorf("failed to load bootstrap configuration: %w", err)
	}

	timeout := configuration.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	bootstrapCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dbClient, err := sql.Open(opts.DbDriver, opts.DbMasterUrl)
	if err != nil {
		return fmt.Errorf("failed to open database connection: %w", err)
	}
	defer dbClient.Close()

	logger.Debug().Str("service", opts.ServiceName).Msg("Waiting for lock")
	unlock, err := database.AcquireLock(bootstrapCtx, dbClient, opts.ServiceName, opts.DbDriver)
	if err != nil {
		return err
	}

	defer func() {
		logger.Debug().Msg("Unlocking")
		if uerr := unlock(); uerr != nil {
			err = errors.Join(err, fmt.Errorf("release lock: %w", uerr))
		}
	}()

	logger.Info().Msg("Bootstrapping Temporal")
	if err := temporal.Run(bootstrapCtx); err != nil {
		return fmt.Errorf("cannot bootstrap Temporal: %w", err)
	}

	return nil
}

// bootstrapMinIO initializes MinIO by ensuring the configured bucket exists.
// This bootstrap does not require database locking as it only performs
// idempotent S3 operations.
func bootstrapMinIO(ctx context.Context, opts Options) error {
	logger := log.ForContext(ctx)

	_, configuration, err := env.Load[Configuration](ctx)
	if err != nil {
		return fmt.Errorf("failed to load bootstrap configuration: %w", err)
	}

	timeout := configuration.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	bootstrapCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	logger.Info().Msg("Bootstrapping MinIO")
	if err := minio.Run(bootstrapCtx); err != nil {
		return fmt.Errorf("cannot bootstrap MinIO: %w", err)
	}

	return nil
}

// loadConfig loads the bootstrap configuration from a file if specified,
// otherwise uses the provided default configuration bytes.
func loadConfig(ctx context.Context, configFile string, defaultConfig []byte) (*BootstrapConfig, error) {
	var (
		logger          = log.ForContext(ctx)
		configBytes     []byte
		bootstrapConfig BootstrapConfig
		err             error
	)

	if configFile != "" {
		logger.Debug().Str("file", configFile).Msg("Loading bootstrap config from file")
		configBytes, err = os.ReadFile(configFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read bootstrap config file: %w", err)
		}
	} else {
		logger.Debug().Msg("Loading embedded bootstrap config")
		configBytes = defaultConfig
	}

	if err = yaml.Unmarshal(configBytes, &bootstrapConfig); err != nil {
		return nil, fmt.Errorf("failed to unmarshal bootstrap config: %w", err)
	}

	return &bootstrapConfig, nil
}
