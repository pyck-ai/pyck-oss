package core

import (
	"context"

	"github.com/pyck-ai/pyck/backend/bootstrap/pkg/bootstrap"
	"github.com/pyck-ai/pyck/backend/common/env"
	envconfig "github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/otel"
	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
)

// FrontendConfig holds environment variables for the frontend settings endpoint.
type FrontendConfig struct {
	AppURL           string `env:"PYCK_FRONTEND_APP_URL" envDefault:"http://localhost:3000"`
	AuthURL          string `env:"PYCK_FRONTEND_AUTH_URL" envDefault:"http://localhost:8080"`
	Environment      string `env:"PYCK_FRONTEND_ENVIRONMENT" envDefault:"development"`
	ClientID         string `env:"PYCK_FRONTEND_CLIENT_ID"`
	RedirectURI      string `env:"PYCK_FRONTEND_REDIRECT_URI" envDefault:"http://localhost:3000/auth/oauth2/callback/zitadel"`
	Debug            bool   `env:"PYCK_FRONTEND_DEBUG" envDefault:"true"`
	Features         string `env:"PYCK_FRONTEND_FEATURES" envDefault:"{}"`
	Version          string `env:"PYCK_FRONTEND_VERSION" envDefault:"unknown"`
	OtelURL          string `env:"PYCK_FRONTEND_OTEL_URL"`
	OtelKey          string `env:"PYCK_FRONTEND_OTEL_INGEST_KEY"`
	FeedbackEndpoint string `env:"PYCK_FRONTEND_FEEDBACK_ENDPOINT"`
	BarcodeApiURL    string `env:"PYCK_FRONTEND_BARCODE_API_URL"`
}

type config struct {
	envconfig.DbConfig
	envconfig.EnvironmentConfig
	envconfig.IdempotencyConfig
	envconfig.EventOutboxConfig
	envconfig.GraphQLConfig
	envconfig.HTTPConfig
	envconfig.LogConfig
	envconfig.NatsConfig
	envconfig.ServiceConfig
	envconfig.ServiceInstanceConfig
	envconfig.TemporalConfig
	envconfig.ZitadelConfig

	// Temporal Worker Deployment Versioning (#1132). Off unless a build ID is
	// injected: the service image carries no module version.
	commonworkflow.VersioningConfig

	FrontendConfig

	otel.OTelConfig

	ZitadelServiceKeyPath string `env:"PYCK_ZITADEL_SERVICE_KEYFILE,notEmpty,required"`

	ZitadelSyncEvery string `env:"PYCK_ZITADEL_SYNC_EVERY,notEmpty" envDefault:"10m"`

	// TenantReconcileInterval is how often the tenant-reconcile workflow
	// heals DB ↔ Zitadel drift (deleted_at vs org state). Lower = drift
	// closes faster, more list-orgs calls to Zitadel.
	TenantReconcileInterval string `env:"PYCK_TENANT_RECONCILE_INTERVAL,notEmpty" envDefault:"5m"`

	// TenantExpiryCheckInterval is how often the tenant-expiry-check
	// workflow scans for tenants whose expires_at has passed and
	// soft-deletes them. Lower = tighter expiry pickup, more DB sweeps.
	TenantExpiryCheckInterval string `env:"PYCK_TENANT_EXPIRY_CHECK_INTERVAL,notEmpty" envDefault:"1m"`

	// Shared HMAC key Zitadel uses to sign Actions v2 webhook callbacks
	// (e.g. /webhook/zitadel/actions/pre-token). Generated and exported by
	// the bootstrap-zitadel container; management refuses to start without
	// it. Every inbound webhook request is signature-verified — there is no
	// empty-key bypass.
	ZitadelActionSigningKey string `env:"PYCK_ZITADEL_ACTION_SIGNING_KEY,notEmpty,required" json:"-"`

	// HMAC key for the login-event webhook (/webhook/zitadel/login), minted by
	// bootstrap-zitadel. Optional: when empty the route isn't mounted, so a
	// deploy can roll out management before the key lands in its secret. When
	// mounted, every request is verified.
	ZitadelLoginActionSigningKey string `env:"PYCK_ZITADEL_LOGIN_ACTION_SIGNING_KEY" json:"-"`

	NatsAuthKeySeed string `env:"PYCK_NATS_AUTH_KEY_SEED,notEmpty,required" json:"-"`

	DynamicSchemaChecks bool `env:"PYCK_DYNAMIC_SCHEMA_CHECKS,notEmpty" envDefault:"false"`

	OpenAiToken string `env:"PYCK_OPENAI_TOKEN"`

	GithubClientID     string `env:"PYCK_GITHUB_CLIENT_ID"`
	GithubClientSecret string `env:"PYCK_GITHUB_CLIENT_SECRET" json:"-"`

	BootstrapEnabled bool `env:"PYCK_BOOTSTRAP_ENABLED" envDefault:"true"`
	BootstrapOnly    bool `env:"PYCK_BOOTSTRAP_ONLY" envDefault:"true"`

	// WorkerAPIURL is the worker cluster's deployment control plane. Required
	// wherever pyck-go tenants are registered: without it their registration
	// fails rather than producing a tenant with no worker.
	WorkerAPIURL string `env:"PYCK_WORKER_API_URL"`
}

type bootstrapConfig struct {
	envconfig.DbConfig
	envconfig.LogConfig

	BootstrapEnabled bool                      `env:"PYCK_BOOTSTRAP_ENABLED" envDefault:"true"`
	BootstrapOnly    bool                      `env:"PYCK_BOOTSTRAP_ONLY" envDefault:"true"`
	BootstrapModule  bootstrap.BootstrapModule `env:"PYCK_BOOTSTRAP_MODULE"`
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
