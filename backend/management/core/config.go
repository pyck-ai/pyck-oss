package core

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/pyck-ai/pyck/backend/common/env"
	envconfig "github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/otel"
)

type config struct {
	envconfig.DbConfig
	envconfig.EnvironmentConfig
	envconfig.EventOutboxConfig
	envconfig.GraphQLConfig
	envconfig.HTTPConfig
	envconfig.LogConfig
	envconfig.NatsConfig
	envconfig.ServiceConfig
	envconfig.ServiceInstanceConfig
	envconfig.TemporalConfig
	envconfig.ZitadelConfig

	otel.OTelConfig

	ZitadelServiceKeyPath string `env:"PYCK_ZITADEL_SERVICE_KEYFILE,notEmpty,required"`
	ZitadelSyncEvery      string `env:"PYCK_ZITADEL_SYNC_EVERY,notEmpty" envDefault:"10m"`

	NatsAuthKeySeed string `env:"PYCK_NATS_AUTH_KEY_SEED,notEmpty,required" json:"-"`

	DynamicSchemaChecks bool `env:"PYCK_DYNAMIC_SCHEMA_CHECKS,notEmpty" envDefault:"false"`

	OpenAiToken string `env:"PYCK_OPENAI_TOKEN"`

	GithubClientID     string `env:"PYCK_GITHUB_CLIENT_ID"`
	GithubClientSecret string `env:"PYCK_GITHUB_CLIENT_SECRET" json:"-"`

	BootstrapEnabled bool `env:"PYCK_BOOTSTRAP_ENABLED" envDefault:"true"`

	FrontendBaseURL string `env:"PYCK_FRONTEND_BASE_URL"`

	// Flavour Go worker image (used separately as the container image)
	FlavourGoWorkerImage    string `env:"PYCK_FLAVOUR_GO_WORKER_IMAGE"`
	FlavourGoWorkerReplicas int32  `env:"PYCK_FLAVOUR_GO_WORKER_REPLICAS" envDefault:"2"`

	// Quickwit configuration
	QuickwitEnabled      bool          `env:"PYCK_QUICKWIT_ENABLED" envDefault:"false"`
	QuickwitURL          string        `env:"PYCK_QUICKWIT_URL"`
	QuickwitBatchSize    int           `env:"PYCK_QUICKWIT_BATCH_SIZE" envDefault:"100"`
	QuickwitBatchTimeout time.Duration `env:"PYCK_QUICKWIT_BATCH_TIMEOUT" envDefault:"5s"`
}

type bootstrapConfig struct {
	envconfig.EnvironmentConfig
	envconfig.ServiceInstanceConfig
	envconfig.TemporalConfig
	envconfig.TemporalBootstrapConfig
}

// TODO(michael): Expose this via context instead of global variable. This would
// make testing easier and promote passing along root context. The basic service
// dependencies can then be centralized via NewService[Config](ctx), which can
// take care of all the common service components like logging, auth, database,
// nats, etc...
var (
	Config          config
	BootstrapConfig bootstrapConfig
)

func LoadEnv() (err error) {
	_, Config, err = env.Load[config](context.TODO())

	return err
}

func LoadBootstrapEnv() (err error) {
	_, BootstrapConfig, err = env.Load[bootstrapConfig](context.TODO())
	return err
}

const flavourGoEnvPrefix = "PYCK_FLAVOUR_GO_"

// FlavourGoWorkerEnvVars collects all PYCK_FLAVOUR_GO_* env vars,
// strips the prefix, and returns them as a map. WORKER_IMAGE is
// excluded since it's used as the container image, not an env var.
func FlavourGoWorkerEnvVars() map[string]string {
	result := make(map[string]string)
	for _, e := range os.Environ() {
		key, value, ok := strings.Cut(e, "=")
		if !ok || !strings.HasPrefix(key, flavourGoEnvPrefix) {
			continue
		}
		stripped := strings.TrimPrefix(key, flavourGoEnvPrefix)
		if stripped == "WORKER_IMAGE" {
			continue
		}
		result[stripped] = value
	}
	return result
}
