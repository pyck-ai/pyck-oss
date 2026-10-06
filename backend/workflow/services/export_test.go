package services

import (
	"context"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	"github.com/pyck-ai/pyck/backend/workflow/model"
)

// ActiveWorkflowsWithSignals exposes the router's live-subscription filter to
// the external test package.
func (wr *SignalRouter) ActiveWorkflowsWithSignals(ctx context.Context, tenantID uuid.UUID) ([]*ent.Workflow, error) {
	return wr.activeWorkflowsWithSignals(ctx, tenantID)
}

// HandleTemporalWorkflowStateChange exposes the state-change handler to the
// external test package.
func (wr *SignalRouter) HandleTemporalWorkflowStateChange(ctx context.Context, msg *nats.Msg) ([]*model.TemporalWorkflow, error) {
	return wr.handleTemporalWorkflowStateChange(ctx, msg)
}

// SetDispatch replaces the handler the consumer calls for each event.
func (wr *SignalRouter) SetDispatch(f func(ctx context.Context, msg *nats.Msg) error) {
	wr.dispatch = f
}

// Permanent marks err as permanent, as the router's own handlers do.
func Permanent(err error) error { return permanent(err) }

// EventsGivenUp exposes the give-up counter.
func EventsGivenUp() prometheus.Counter { return eventsGivenUp }

// IsPermanent exposes the error classification to the external test package.
func IsPermanent(err error) bool { return isPermanent(err) }

// IsDependencyOutage exposes the outage classification to the external test package.
func IsDependencyOutage(err error) bool { return isDependencyOutage(err) }

// IsRateLimited exposes the rate-limit classification to the external test package.
func IsRateLimited(err error) bool { return isRateLimited(err) }

// IsTemporalUnavailable exposes the per-event Temporal Unavailable classification to the external test package.
func IsTemporalUnavailable(err error) bool { return isTemporalUnavailable(err) }

// RouterPaused exposes the paused gauge.
func RouterPaused() prometheus.Gauge { return routerPaused }

// Paused reports whether this router's health gate is closed. Unlike the
// process-global paused gauge it is not affected by other tests' routers.
func (wr *SignalRouter) Paused() bool {
	run := wr.run

	return run != nil && run.gate.isTripped()
}

// PendingRoutingCollectors is the number of routing collectors of events that
// are being handled or were NAKed and not yet redelivered.
func (wr *SignalRouter) PendingRoutingCollectors() int {
	wr.routing.mu.Lock()
	defer wr.routing.mu.Unlock()

	return len(wr.routing.pending)
}

// WithDelivery exposes the delivery count of the event being handled, which
// the consumer sets.
func WithDelivery(ctx context.Context, delivered uint64) context.Context {
	return withDelivery(ctx, delivered)
}

// TrackRouting puts a routing collector on ctx, as the consumer does for a
// settled event, and CollectedTargets reads what it gathered.
func TrackRouting(ctx context.Context) context.Context {
	return context.WithValue(ctx, routingCollectorKey{}, &routingCollector{})
}

// CollectedTargets returns the targets recorded on a TrackRouting context.
func CollectedTargets(ctx context.Context) []RoutingTarget {
	return routingFrom(ctx).snapshot()
}

// DispatchEvent exposes the consumer's per-event routing by subject.
func (wr *SignalRouter) DispatchEvent(ctx context.Context, msg *nats.Msg) error {
	return wr.dispatchEvent(ctx, msg)
}

// SystemNamespaceStateChange exposes the system-namespace subject check.
func SystemNamespaceStateChange(subject string) (string, bool) {
	return systemNamespaceStateChange(subject)
}
