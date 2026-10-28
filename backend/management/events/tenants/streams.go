package tenants

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/log"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
)

// Each sweep runs on one replica; the others keep their last values, so an
// alert reads the missing count of the replica with the newest timestamp.
var (
	tenantStreamsMissing = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "management_tenant_event_streams_missing",
		Help: "Active tenants whose events reach no service: the last stream reconcile or repair could not put their stream, or the aggregate does not source it",
	})
	tenantStreamsChecked = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "management_tenant_event_streams_checked_timestamp_seconds",
		Help: "When this replica last set management_tenant_event_streams_missing",
	})
)

func recordStreams(repair events.StreamRepair) {
	tenantStreamsMissing.Set(float64(repair.Failed + repair.Unsourced))
	tenantStreamsChecked.SetToCurrentTime()
}

// ReconcileStreams gives every active tenant its event stream. Call it before
// the outbox starts. Soft-deleted tenants get none (restoreTenant puts it) but
// keep one they have. Partial failures are logged, not returned, so the other
// tenants' events still flow; RepairStreams retries them.
func ReconcileStreams(ctx context.Context, client *ent.Client, topology *events.StreamTopology) error {
	tenantIDs, err := activeTenantIDs(ctx, client)
	if err != nil {
		return err
	}

	repair, err := topology.Reconcile(ctx, tenantIDs)
	if err != nil && !isPartial(err) {
		return fmt.Errorf("reconcile the event streams of %d tenants: %w", len(tenantIDs), err)
	}

	recordStreams(repair)

	logPartial(ctx, err, len(tenantIDs))

	return nil
}

// RepairStreams puts the missing streams of active tenants, on every tenant
// reconcile sweep, so a stream lost after startup does not wait for a restart.
// Partial failures are logged, not returned; the next sweep retries them.
func RepairStreams(ctx context.Context, client *ent.Client, topology *events.StreamTopology) error {
	tenantIDs, err := activeTenantIDs(ctx, client)
	if err != nil {
		return err
	}

	repair, err := topology.RepairTenants(ctx, tenantIDs)
	if err != nil && !isPartial(err) {
		return fmt.Errorf("repair the event streams of %d tenants: %w", len(tenantIDs), err)
	}

	recordStreams(repair)

	if repair.Missing > 0 {
		log.ForContext(ctx).Warn().
			Int("missing", repair.Missing).
			Int("failed", repair.Failed).
			Msg("put the event streams of tenants that had none")
	}

	logPartial(ctx, err, len(tenantIDs))

	return nil
}

func activeTenantIDs(ctx context.Context, client *ent.Client) ([]uuid.UUID, error) {
	sysCtx := authn.Context(ctx, authn.SystemUser())

	tenantIDs, err := client.Tenant.Query().Where(enttenant.DeletedAtIsNil()).PagedIDs(sysCtx, mixin.Limit)
	if err != nil {
		return nil, fmt.Errorf("list active tenants: %w", err)
	}

	return tenantIDs, nil
}

// isPartial reports a failure after which every existing stream is still
// sourced (see events.StreamTopology.Reconcile).
func isPartial(err error) bool {
	return errors.Is(err, events.ErrTenantStreams) || errors.Is(err, events.ErrAggregateTooLarge)
}

func logPartial(ctx context.Context, err error, tenants int) {
	if err == nil {
		return
	}

	log.ForContext(ctx).Error().Err(err).
		Int("tenants", tenants).
		Msg("some tenant event streams are missing or unsourced")
}
