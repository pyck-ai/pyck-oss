package guards

import (
	"context"
	"time"

	"golang.org/x/sync/errgroup"
)

type (
	// CheckFunc is the function to execute.
	CheckFunc func(context.Context) (bool, error)

	// Check holds the configuration for a single dependency check.
	Check struct {
		// Name is a descriptive name for the check
		Name string

		// CheckFunc is the function to execute.
		CheckFunc CheckFunc

		// RetryInterval is the duration to wait before retrying a failed check.
		RetryInterval time.Duration

		// Timeout is the maximum duration for a single check execution.
		Timeout time.Duration
	}

	// Guard manages a collection of dependency checks and orchestrates their execution.
	Guard struct {
		// Timeout is the maximum duration for all checks to complete.
		Timeout time.Duration
		checks  []Check
	}
)

// New creates a new Guard instance.
func New() *Guard {
	return &Guard{
		checks: []Check{},
	}
}

// WithTimeout sets the global timeout for the Guard.
func (g *Guard) WithTimeout(timeout time.Duration) *Guard {
	g.Timeout = timeout
	return g
}

// Add adds a new check to the Guard.
func (g *Guard) Add(check Check) *Guard {
	if check.RetryInterval == 0 {
		check.RetryInterval = 5 * time.Second
	}
	if check.Timeout == 0 {
		check.Timeout = 5 * time.Second
	}
	g.checks = append(g.checks, check)
	return g
}

// Wait blocks until all dependency checks have passed successfully or until the Guard's context is cancelled.
// It returns nil if all checks passed, or an error if the context was cancelled.
func (g *Guard) Wait(ctx context.Context) error {
	if g.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.Timeout)
		defer cancel()
	}

	eg, groupCtx := errgroup.WithContext(ctx)
	for _, check := range g.checks {
		c := check
		eg.Go(func() error {
			return g.runCheckLoop(groupCtx, c)
		})
	}

	return eg.Wait()
}

func (g *Guard) runCheckLoop(ctx context.Context, c Check) error {
	for {
		success, err := g.performCheck(ctx, c)
		if err != nil {
			return err
		}
		if success {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.RetryInterval):
		}
	}
}

// performCheck runs a single check and returns:
// - bool: true if check passed, false if not
// - error: non-nil if a FATAL error occurred (propagated from CheckFunc)
func (g *Guard) performCheck(ctx context.Context, c Check) (bool, error) {
	// Create context with timeout for this specific check
	checkCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	return c.CheckFunc(checkCtx)
}
