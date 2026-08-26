package workflowsdk

import (
	"context"
	"time"

	"github.com/pyck-ai/pyck/backend/common/env"
	envconfig "github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/otel"
	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
)

type WorkflowConfig struct {
	WorkflowMocking bool `env:"PYCK_WORKFLOW_MOCKING,notEmpty"`
}

// RegistrationConfig tunes how the worker keeps its signal subscriptions
// registered with the workflow service.
type RegistrationConfig struct {
	// HeartbeatInterval is how often the worker refreshes its subscription TTLs.
	// It must be shorter than the service's PYCK_WORKFLOW_SUBSCRIPTION_TTL;
	// non-positive disables the heartbeat.
	HeartbeatInterval time.Duration `env:"PYCK_WORKER_REGISTRATION_HEARTBEAT_INTERVAL" envDefault:"5m"`
	// RegistrationRetryAttempts is how many times the initial registration is
	// tried on a transient serialization/deadlock conflict before giving up.
	RegistrationRetryAttempts int `env:"PYCK_WORKER_REGISTRATION_RETRY_ATTEMPTS" envDefault:"5"`
	// RegistrationRetryBackoff is the base delay for the exponential backoff
	// between registration retries.
	RegistrationRetryBackoff time.Duration `env:"PYCK_WORKER_REGISTRATION_RETRY_BACKOFF" envDefault:"1s"`
}

// WorkerVersioningConfig configures Temporal Worker Deployment Versioning (#1132):
// each worker registers a deployment version so rolling deploys don't break
// in-flight executions.
type WorkerVersioningConfig struct {
	commonworkflow.VersioningConfig

	// Bundle slug stamped for every workflow type this worker serves (#1317).
	// Empty for per-tenant bundles, which are keyed by version alone.
	UIBundleSlug string `env:"PYCK_UI_BUNDLE_SLUG"`
	// Version segment of the UI bundle URL: the identifier the bundle was
	// uploaded under, a git SHA. Distinct from the build ID, which the
	// controller derives from the pod template.
	UIBundleVersion string `env:"PYCK_UI_BUNDLE_VERSION"`
}

type config struct {
	envconfig.EnvironmentConfig
	envconfig.GatewayConfig
	envconfig.LogConfig
	otel.OTelConfig
	RegistrationConfig
	WorkerVersioningConfig
}

var Config config

func LoadEnv(ctx context.Context) error {
	_, c, err := env.Load[config](ctx)
	if err != nil {
		return err
	}

	Config = c

	return nil
}
