package services

import (
	"context"
	"fmt"
	"time"

	entprivacy "entgo.io/ent/privacy"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/request"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	entpredicate "github.com/pyck-ai/pyck/backend/workflow/ent/gen/predicate"
	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
)

// SubscriptionJanitor periodically hard-deletes signal subscriptions whose TTL
// has lapsed: the rows a worker leaves behind when it stops refreshing (crash,
// scale-down).
//
// It never deletes workflow rows. A workflow whose subscriptions have all
// expired (its workers are down, or a rollout is in progress) keeps its row, so
// the workers can register it again and its name stays reserved; the only ways
// to remove a workflow are deleteWorkflow and tenant teardown. Inferring "nobody
// serves this any more" from Temporal is not safe: poller history is in memory
// with a short TTL, so an outage or a Temporal restart looks the same as a
// retired task queue (pyck#1564).
type SubscriptionJanitor struct {
	client   *ent.Client
	interval time.Duration
}

// NewSubscriptionJanitor returns a janitor that sweeps every interval once started.
func NewSubscriptionJanitor(client *ent.Client, interval time.Duration) *SubscriptionJanitor {
	return &SubscriptionJanitor{client: client, interval: interval}
}

// Start spawns the sweep goroutine and returns immediately; cancel ctx to stop.
func (j *SubscriptionJanitor) Start(ctx context.Context) {
	go j.run(ctx)
}

func (j *SubscriptionJanitor) run(ctx context.Context) {
	logger := log.ForContext(ctx)
	logger.Info().Dur("interval", j.interval).Msg("subscription janitor started")

	t := time.NewTicker(j.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info().Msg("subscription janitor stopping")
			return
		case <-t.C:
			n, err := j.Sweep(ctx)
			if err != nil {
				logger.Error().Err(err).Msg("subscription janitor sweep failed")
			} else if n > 0 {
				logger.Debug().Int("reaped", n).Msg("subscription janitor reaped expired subscriptions")
			}
		}
	}
}

// Sweep deletes every expired subscription across all tenants in one statement.
// Reaping is cross-tenant maintenance: it runs as the system user (TenantMixin
// leaves bulk deletes unscoped), suppresses CRUD events, and bypasses the
// privacy filters via entprivacy.Allow.
func (j *SubscriptionJanitor) Sweep(ctx context.Context) (int, error) {
	ctx = request.Context(ctx, authn.SystemUser(), uuid.Nil)
	ctx = feature.Context(ctx, feature.FEATURE_SUPPRESS_EVENTS)
	ctx = entprivacy.DecisionContext(ctx, entprivacy.Allow)

	n, err := j.client.WorkflowSignal.Delete().
		Where(
			entworkflowsignal.ExpiresAtLT(time.Now().UTC()),
		).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("reap expired subscriptions: %w", err)
	}
	return n, nil
}

// LiveSignal matches a subscription that is still routing: not deleted and not
// past its TTL. A stopped subscription (stopped_at set by unregisterWorker) is
// still live until its TTL lapses; see PreferRunning for how the router ranks it.
func LiveSignal(now time.Time) entpredicate.WorkflowSignal {
	return entworkflowsignal.And(
		entworkflowsignal.DeletedAtIsNil(),
		entworkflowsignal.ExpiresAtGT(now),
	)
}

// PreferRunning picks, from one workflow's live subscriptions, the ones that
// should route. This is the read-time half of a clean worker stop (the Temporal
// pattern: a clean stop is a hint, a crash is handled by the TTL, the decision
// is made per event):
//
//   - If any live subscription is not stopped (stopped_at is nil), only those
//     route. Their owners are running, or crashed and not yet expired; either
//     way they are the best information available, and subscriptions of workers
//     that shut down cleanly (an old version in a rolling deploy) must not route
//     alongside them.
//   - Otherwise every live subscription routes. The workers that stopped
//     cleanly are the last known owners, so their subscriptions bridge the gap
//     until a replacement registers or their TTL lapses.
//
// The input must already be filtered by LiveSignal. The result may alias the
// input slice.
func PreferRunning(signals []*ent.WorkflowSignal) []*ent.WorkflowSignal {
	running := make([]*ent.WorkflowSignal, 0, len(signals))
	for _, s := range signals {
		if s.StoppedAt == nil {
			running = append(running, s)
		}
	}
	if len(running) == 0 {
		return signals
	}
	return running
}
