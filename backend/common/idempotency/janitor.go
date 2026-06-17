package idempotency

import (
	"context"
	"time"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// Janitor periodically prunes committed idempotency records older than the
// configured TTL. It mirrors the pattern of common/events.ReplyRegistry —
// owned by the host service and stopped when the root context is
// cancelled.
type Janitor struct {
	store    Store
	interval time.Duration
	ttl      time.Duration
}

// NewJanitor constructs a Janitor. interval is how often the goroutine
// runs; ttl is how long a committed record is kept before it is eligible
// for pruning.
func NewJanitor(store Store, interval, ttl time.Duration) *Janitor {
	return &Janitor{store: store, interval: interval, ttl: ttl}
}

// Start spawns the prune goroutine and returns immediately. Mirrors the
// convention of [common/events.ReplyRegistry.Start]: the lifecycle is
// owned by the supplied ctx, and cancelling it terminates the
// goroutine without needing a separate Stop() call. Production callers
// should use this; tests that need to observe loop completion can call
// [Run] directly (it is exported for exactly that purpose).
func (j *Janitor) Start(ctx context.Context) {
	go j.Run(ctx)
}

// Run is the blocking prune loop. Cancel ctx to terminate. Exported so
// tests can wait for clean shutdown via a `done` channel:
//
//	go func() { j.Run(ctx); close(done) }()
//
// Production code should call [Start] instead.
func (j *Janitor) Run(ctx context.Context) {
	logger := log.ForContext(ctx)
	logger.Info().
		Dur("interval", j.interval).
		Dur("ttl", j.ttl).
		Msg("idempotency janitor started")

	t := time.NewTicker(j.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info().Msg("idempotency janitor stopping")
			return
		case <-t.C:
			cutoff := time.Now().UTC().Add(-j.ttl)
			n, err := j.store.Prune(ctx, cutoff)
			if err != nil {
				logger.Error().
					Err(err).
					Time("cutoff", cutoff).
					Msg("idempotency janitor prune failed")
				continue
			}
			if n > 0 {
				logger.Debug().
					Int("pruned", n).
					Time("cutoff", cutoff).
					Msg("idempotency janitor pruned records")
			}
		}
	}
}
