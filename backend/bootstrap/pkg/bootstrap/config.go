package bootstrap

import (
	_ "embed"
	"errors"
	"time"

	"github.com/pyck-ai/pyck/backend/bootstrap/internal/zitadel"
)

var (
	// ErrNoBootstrapModule is returned when no bootstrap module is specified.
	ErrNoBootstrapModule = errors.New("no bootstrap module specified")

	// ErrUnknownBootstrapModule is returned when an invalid bootstrap module name is specified.
	ErrUnknownBootstrapModule = errors.New("unknown bootstrap module")
)

type (
	// Configuration holds bootstrap-specific settings loaded from environment variables.
	Configuration struct {
		// Timeout for the entire bootstrap process (default: 5m).
		Timeout time.Duration `env:"PYCK_BOOTSTRAP_TIMEOUT"`

		// ConfigFile is the path to an external bootstrap configuration file.
		// When empty, DefaultConfig from Options is used.
		ConfigFile string `env:"PYCK_BOOTSTRAP_CONFIG_FILE"`
	}

	// Options configures the bootstrap process with caller-specific settings
	// that cannot be derived from the environment.
	Options struct {
		// ServiceName is used for generating the advisory lock ID.
		ServiceName string

		// DbMasterUrl is the database connection string for advisory locking.
		DbMasterUrl string

		// DbDriver is the database dialect (e.g. dialect.Postgres, dialect.SQLite).
		DbDriver string

		// DefaultConfig is the fallback bootstrap configuration (JSON), typically
		// provided via go:embed. Used when no config file is specified via
		// PYCK_BOOTSTRAP_CONFIG_FILE.
		DefaultConfig []byte
	}

	// BootstrapConfig is the root configuration for seeding Zitadel.
	BootstrapConfig struct {
		Zitadel zitadel.Zitadel `yaml:"zitadel"`
	}
)
