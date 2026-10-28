package workerapi

import (
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// ErrInvalidRetryConfig is returned by RetryConfig.Validate.
var ErrInvalidRetryConfig = errors.New("invalid worker-api retry config")

// RetryConfig is the budget an activity gets for one worker-api call: short
// attempts, retried until ScheduleToClose.
type RetryConfig struct {
	StartToClose    time.Duration `env:"PYCK_WORKER_API_ACTIVITY_START_TO_CLOSE" envDefault:"30s"`
	ScheduleToClose time.Duration `env:"PYCK_WORKER_API_ACTIVITY_SCHEDULE_TO_CLOSE" envDefault:"5m"`
	InitialInterval time.Duration `env:"PYCK_WORKER_API_RETRY_INITIAL_INTERVAL" envDefault:"2s"`
	MaximumInterval time.Duration `env:"PYCK_WORKER_API_RETRY_MAXIMUM_INTERVAL" envDefault:"30s"`
}

// Validate rejects zero or negative durations (Temporal refuses an activity
// without a positive timeout, which would fail every lifecycle workflow) and
// a StartToClose that does not leave room for the client's own request
// timeout, which would turn a slow worker-api into an activity timeout
// instead of a transport error.
func (c RetryConfig) Validate() error {
	for name, d := range map[string]time.Duration{
		"StartToClose": c.StartToClose, "ScheduleToClose": c.ScheduleToClose,
		"InitialInterval": c.InitialInterval, "MaximumInterval": c.MaximumInterval,
	} {
		if d <= 0 {
			return fmt.Errorf("%w: %s is %s, must be positive", ErrInvalidRetryConfig, name, d)
		}
	}

	if c.StartToClose <= requestTimeout {
		return fmt.Errorf("%w: StartToClose %s must exceed the request timeout %s",
			ErrInvalidRetryConfig, c.StartToClose, requestTimeout)
	}

	return nil
}

// ActivityOptions builds the Temporal options for an activity that calls
// worker-api. Attempts are bounded by ScheduleToClose, not by a count.
func ActivityOptions(cfg RetryConfig) workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout:    cfg.StartToClose,
		ScheduleToCloseTimeout: cfg.ScheduleToClose,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    cfg.InitialInterval,
			BackoffCoefficient: 2.0,
			MaximumInterval:    cfg.MaximumInterval,
		},
	}
}
