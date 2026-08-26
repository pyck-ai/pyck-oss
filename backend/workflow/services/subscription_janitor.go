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
	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
)

// SubscriptionJanitor periodically hard-deletes worker-owned signal
// subscriptions whose TTL has lapsed — the rows a worker leaves behind when it
// stops refreshing (crash, scale-down). Legacy shared subscriptions have no
// expiry and are never touched.
type SubscriptionJanitor struct {
	client   *ent.Client
	interval time.Duration
}

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
				continue
			}
			if n > 0 {
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
			entworkflowsignal.ExpiresAtNotNil(),
			entworkflowsignal.ExpiresAtLT(time.Now().UTC()),
		).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("reap expired subscriptions: %w", err)
	}
	return n, nil
}
