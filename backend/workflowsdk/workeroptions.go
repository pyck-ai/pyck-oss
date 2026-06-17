package workflowsdk

import (
	"time"

	temporalclient "go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
)

type WorkerOption func(*worker)

func WithClientOptions(opts temporalclient.Options) WorkerOption {
	return func(w *worker) {
		w.clientOptions = opts
	}
}

func WithWorkerOptions(opts temporalworker.Options) WorkerOption {
	return func(w *worker) {
		w.workerOptions = opts
	}
}

// HealthServerOption mutates the health server config. Used inside
// WithHealthServer to override env-derived defaults from code.
type HealthServerOption func(*healthServerConfig)

// WithHealthServer enables the /health endpoint (default-on already;
// use this only to override env-derived defaults from code, or to
// re-enable when HEALTH_DISABLED=1 is set).
func WithHealthServer(opts ...HealthServerOption) WorkerOption {
	return func(w *worker) {
		cfg := w.healthConfig
		cfg.Enabled = true
		for _, opt := range opts {
			opt(&cfg)
		}
		w.healthConfig = cfg
	}
}

// WithoutHealthServer disables the /health endpoint. Use for short-lived
// or test workers where binding a port would be a nuisance.
func WithoutHealthServer() WorkerOption {
	return func(w *worker) {
		w.healthConfig.Enabled = false
	}
}

func HealthPort(port int) HealthServerOption {
	return func(c *healthServerConfig) { c.Port = port }
}

func HealthInterval(d time.Duration) HealthServerOption {
	return func(c *healthServerConfig) { c.Interval = d }
}

func HealthTimeout(d time.Duration) HealthServerOption {
	return func(c *healthServerConfig) { c.Timeout = d }
}

func HealthMaxStale(d time.Duration) HealthServerOption {
	return func(c *healthServerConfig) { c.MaxStale = d }
}
