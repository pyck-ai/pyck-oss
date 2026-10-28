// Package tenants subscribes to management's tenant CRUD events and
// dispatches the corresponding tenant lifecycle workflow, and reconciles the
// tenants' event streams (ReconcileStreams).
//
// Trigger flow:
//  1. Resolver does tx.Tenant.Update(SetDeletedAt | ClearDeletedAt)
//  2. Outbox hook emits pyck.<tenant>.crud.management.tenant.<id>.updated
//  3. SubscribeTrigger receives the event
//  4. Compares deleted_at before/after and starts DisableTenantWorkflow
//     or RestoreTenantWorkflow
//
// Registration flow:
//  1. registerTenant resolver commits a status=pending tenant row
//  2. Outbox hook emits pyck.<tenant>.crud.management.tenant.<id>.create
//  3. SubscribeTrigger starts RegisterTenantWorkflow for the row under
//     the deterministic ID the resolver is waiting on
//
// Update and delete events dispatch regardless of the row's status: a
// pending or error row may already own a Zitadel org (the registration
// run creates it before the status flip), and the lifecycle workflows
// tolerate an org that was never created (disable treats NotFound as
// done, restore fails with ErrZitadelOrgNotFound).
package tenants

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/events/topic"
	"github.com/pyck-ai/pyck/backend/common/log"

	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	"github.com/pyck-ai/pyck/backend/management/workflows"
	disabletenant "github.com/pyck-ai/pyck/backend/management/workflows/disable-tenant"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
	restoretenant "github.com/pyck-ai/pyck/backend/management/workflows/restore-tenant"
)

// triggerConsumerName is the durable of the tenant-lifecycle trigger.
//
// The "-v2" suffix retires the durable that existed before the create
// subject joined the filter. A durable's filter is not versioned: during a
// rolling deploy the old and the new binary share one consumer, and a
// create event delivered to a replica running the old filter is ACKed
// without starting a registration: the tenant row stays pending with no
// run behind it. A new durable gives the new binaries a consumer the old
// ones never read.
//
// DeliverNewPolicy because this consumer's update and delete handling
// derives the lifecycle transition from the event payload, not from the
// row: starting at the beginning of the stream's retained history would
// re-dispatch every disable and restore the platform has ever performed.
// Only events published after the consumer exists are of interest; a
// create event that predates it is never delivered, so a change of durable
// is deployed with no registration in flight.
//
// During the overlap both durables see update and delete events, so both
// dispatch the same lifecycle workflow. That is idempotent by workflow ID:
// the lifecycle starts use TERMINATE_EXISTING and the registration start
// treats WorkflowExecutionAlreadyStarted as success. The old durable is
// left in place; JetStream removes it after its InactiveThreshold, or an
// operator removes it with `nats consumer rm`.
const triggerConsumerName = "management-tenant-lifecycle-trigger-v2"

// SubscribeTrigger creates a JetStream consumer for tenant CRUD events.
// Update and delete events dispatch the lifecycle workflow (disable or
// restore) matching the observed deleted_at transition; the create event
// of a status=pending row starts the RegisterTenantWorkflow that
// provisions it.
//
// Called at management service startup. Returns an error if the consumer
// creation or subscription fails; caller should log and abort startup.
//
//nolint:ireturn // jetstream.ConsumeContext is the cancel handle the caller needs.
func SubscribeTrigger(
	ctx context.Context,
	js jetstream.JetStream,
	temporalClient temporalclient.Client,
	streamName string,
	cfg TriggerConfig,
) (jetstream.ConsumeContext, error) {
	subjects := triggerSubjects(streamName)

	log.ForContext(ctx).Info().
		Strs("subjects", subjects).
		Msg("subscribing to tenant lifecycle trigger events")

	consumer, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Name:           triggerConsumerName,
		Durable:        triggerConsumerName,
		FilterSubjects: subjects,
		AckPolicy:      jetstream.AckExplicitPolicy,
		DeliverPolicy:  jetstream.DeliverNewPolicy,
		MaxDeliver:     cfg.MaxRedeliver,
		AckWait:        cfg.ackWait(),
	})
	if err != nil {
		return nil, fmt.Errorf("create trigger consumer: %w", err)
	}

	// One message per pull: JetStream starts a message's AckWait when it
	// delivers it, and the callback runs serially. With the default
	// prefetch a message queued behind a handler blocked in Temporal would
	// time out while it waits, be redelivered at once without the
	// NakWithDelay schedule and spend a delivery; pulling one at a time
	// starts every AckWait when its handler starts.
	cc, err := consumer.Consume(func(msg jetstream.Msg) {
		handleTriggerMessage(ctx, msg, temporalClient, cfg)
	}, jetstream.PullMaxMessages(triggerPullBatch))
	if err != nil {
		return nil, fmt.Errorf("consume trigger events: %w", err)
	}

	return cc, nil
}

// triggerSubjects lists the tenant CRUD subjects the durable consumer
// filters on. Both "updated" and "deleted" are needed because the outbox
// hook reclassifies SetDeletedAt mutations as soft deletes
// (operation="deleted"): disable triggers "deleted", restore triggers
// "updated". "created" carries the pending row of a registration; without
// it no registration is ever provisioned and every registerTenant call
// ends with an unknown outcome.
func triggerSubjects(streamName string) []string {
	ops := []string{events.OpCreate, events.OpUpdate, events.OpDelete}
	subjects := make([]string, 0, len(ops))
	for _, op := range ops {
		subjects = append(subjects, events.MutationEventTopic{
			StreamName:    streamName,
			ServiceName:   topic.ManagementService,
			SchemaName:    topic.TenantSchema,
			OperationName: op,
		}.String())
	}
	return subjects
}

// handleTriggerMessage processes a single tenant mutation event.
func handleTriggerMessage(
	ctx context.Context,
	msg jetstream.Msg,
	temporalClient temporalclient.Client,
	cfg TriggerConfig,
) {
	var (
		event        events.MutationEventMessage
		workflowName string
		workflowID   string
		input        any
	)

	logger := log.ForContext(ctx).With().
		Str("component", "tenants.trigger").
		Str("subject", msg.Subject()).
		Logger()

	logger.Debug().Msg("received tenant mutation event")

	if err := json.Unmarshal(msg.Data(), &event); err != nil {
		logger.Error().Err(err).Msg("decode mutation event")
		ack(msg, logger)
		return
	}

	// Sanity-check the event matches what we expect to handle.
	// The outbox hook emits "deleted" for disable (soft delete),
	// "updated" for restore (clearing deleted_at) and "created" for a
	// registration's pending row.
	if event.Service != topic.ManagementService || !strings.EqualFold(event.Schema, topic.TenantSchema) {
		ack(msg, logger)
		return
	}
	if event.Operation != events.OpCreate && event.Operation != events.OpUpdate && event.Operation != events.OpDelete {
		ack(msg, logger)
		return
	}

	dataAfter, ok := event.DataAfter.(map[string]any)
	if !ok {
		logger.Debug().Msg("DataAfter is not a map, skipping")
		ack(msg, logger)
		return
	}

	tenantID := event.ID
	if tenantID == uuid.Nil {
		logger.Error().Msg("event has nil tenant ID; cannot dispatch workflow")
		ack(msg, logger)
		return
	}

	// Only the create event of a pending row starts a registration; a
	// row created in any other status has nothing to provision.
	if event.Operation == events.OpCreate {
		status, _ := dataAfter["status"].(string)
		if status != enttenant.StatusPending.String() {
			logger.Debug().Str("status", status).Msg("create event without pending status, skipping")
			ack(msg, logger)
			return
		}
		startRegistration(ctx, logger, msg, temporalClient, tenantID, cfg)
		return
	}

	dataBefore, ok := event.DataBefore.(map[string]any)
	if !ok {
		logger.Debug().Msg("DataBefore is not a map, skipping")
		ack(msg, logger)
		return
	}

	idpOrgRef, ok := dataAfter["idp_org_ref"].(string)
	if !ok || idpOrgRef == "" {
		logger.Error().Msg("event missing idp_org_ref; cannot dispatch workflow")
		ack(msg, logger)
		return
	}

	// Decide disable vs restore by comparing deleted_at before and after.
	// disable: deleted_at was nil/zero → now set  (tenant being soft-deleted)
	// restore: deleted_at was set → now nil/zero  (tenant being restored)
	// unchanged: no lifecycle transition, ignore
	//
	// Ent serializes Go's zero time as "0001-01-01T00:00:00Z" (not null),
	// so we must treat both nil and the zero-time string as "not deleted".
	deletedBefore := topic.IsDeletedAt(dataBefore["deleted_at"])
	deletedAfter := topic.IsDeletedAt(dataAfter["deleted_at"])

	// when the field is unchanged, we skip the workflows
	if deletedBefore == deletedAfter {
		logger.Debug().Msg("deleted_at unchanged")
		ack(msg, logger)
		return
	}

	// Shared workflow ID serializes disable and restore per tenant.
	// TERMINATE_EXISTING enforces "latest user intent wins": an
	// opposite-direction event kills the in-flight workflow and
	// runs the new one in its place. Narrow rather than airtight:
	// the Zitadel activities are idempotent (deactivate INACTIVE /
	// activate ACTIVE are no-ops), the worker stop/resume activities
	// re-read the tenant's deleted_at right before acting and step
	// aside when it no longer matches their direction (a terminated
	// workflow's dispatched activity keeps running), and JetStream
	// per-subject ordering guarantees events arrive in user-intent
	// order. What remains is the in-flight worker-api request: a
	// stop already issued can still land after a resume that
	// overtook it within the client's request timeout.
	workflowID = workflows.TenantLifecycleWorkflowID(tenantID)
	if deletedAfter {
		workflowName = workflows.DisableTenantWorkflow
		input = disabletenant.DisableTenantWorkflowInput{
			TenantID:  tenantID,
			IdpOrgRef: idpOrgRef,
		}
	} else {
		workflowName = workflows.RestoreTenantWorkflow
		input = restoretenant.RestoreTenantWorkflowInput{
			TenantID:  tenantID,
			IdpOrgRef: idpOrgRef,
		}
	}

	logger = logger.With().
		Str("workflow_name", workflowName).
		Str("workflow_id", workflowID).
		Str("tenant_id", tenantID.String()).
		Logger()

	opts := temporalclient.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                workflows.TemporalManagementTaskQueue,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
	}

	callCtx, cancel := context.WithTimeout(ctx, cfg.TemporalCallTimeout)
	defer cancel()
	run, err := temporalClient.ExecuteWorkflow(callCtx, opts, workflowName, input)
	if err != nil {
		// Real failures (Temporal unreachable, malformed input, etc.)
		// NAK so JetStream redelivers; tenant-reconcile is the
		// longer-window safety net.
		logger.Error().Err(err).Msg("failed to start lifecycle workflow")
		nak(logger, msg, cfg.MaxRedeliver)
		return
	}

	logger.Info().
		Str("run_id", run.GetRunID()).
		Msg("lifecycle workflow started")

	ack(msg, logger)
}

// startRegistration starts RegisterTenantWorkflow for the pending row
// under the ID the registerTenant resolver is waiting on. The reuse
// policy admits a new run only while no run exists or the last one
// closed unsuccessfully (failed, timed out, terminated): a redelivered
// create event during or after a successful run is rejected as already
// started (ACK: the same operation is or was in flight), while an
// operator's retry of a failed registration under the same ID stays
// possible. The execution timeout closes a run that cannot make progress
// so it does not hold the ID forever. Any other start failure (Temporal
// unreachable) is NAKed so the redelivery starts it.
func startRegistration(ctx context.Context, logger log.Logger, msg jetstream.Msg, temporalClient temporalclient.Client, tenantID uuid.UUID, cfg TriggerConfig) {
	workflowID := workflows.RegisterTenantWorkflowID(tenantID)
	logger = logger.With().
		Str("workflow_name", workflows.RegisterTenantWorkflow).
		Str("workflow_id", workflowID).
		Str("tenant_id", tenantID.String()).
		Logger()

	opts := temporalclient.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                workflows.TemporalManagementTaskQueue,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
		WorkflowExecutionTimeout: registertenant.ExecutionTimeout,
	}

	callCtx, cancel := context.WithTimeout(ctx, cfg.TemporalCallTimeout)
	defer cancel()
	run, err := temporalClient.ExecuteWorkflow(callCtx, opts, workflows.RegisterTenantWorkflow,
		registertenant.RegisterTenantWorkflowInput{TenantID: tenantID})
	var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &alreadyStarted) {
		logger.Debug().Msg("register tenant workflow already started, dropping redelivered create event")
		ack(msg, logger)
		return
	}
	if err != nil {
		logger.Warn().Err(err).Msg("failed to start register tenant workflow")
		nak(logger, msg, cfg.MaxRedeliver)
		return
	}

	logger.Info().Str("run_id", run.GetRunID()).Msg("register tenant workflow started")
	ack(msg, logger)
}

// ack acknowledges the message; a failed ACK only delays a redelivery the
// handler is idempotent against, so it is logged, not retried.
//
// A twin of the same helper lives in backend/common/authn: that package
// cannot import backend/common/events, which imports backend/common/request,
// which imports authn.
func ack(msg jetstream.Msg, logger log.Logger) {
	if err := msg.Ack(); err != nil {
		logger.Warn().Err(err).Msg("could not ack tenant event")
	}
}

// nak requests redelivery after the redeliverBackoff delay of the
// delivery that failed; a delivery count that cannot be read gets the
// first delay. The last permitted delivery is logged at Error: JetStream
// drops the message silently after maxDeliver deliveries, and a dropped
// create event leaves its tenant row pending with no run until an
// operator starts one.
//
// maxDeliver is the consumer's MaxDeliver. A non-positive one is not a cap
// at all (JetStream reads it as unlimited redelivery) so the exhaustion
// line is skipped rather than emitted against a wrapped-around bound.
func nak(logger log.Logger, msg jetstream.Msg, maxDeliver int) {
	delay := redeliverBackoff[0]
	meta, err := msg.Metadata()
	switch {
	case err != nil:
		logger.Warn().Err(err).Msg("could not read delivery count of tenant event")
	case maxDeliver > 0 && meta.NumDelivered >= uint64(maxDeliver):
		logger.Error().Uint64("deliveries", meta.NumDelivered).
			Msg("tenant event exhausted its redeliveries and is dropped; a create event leaves its tenant row pending with no run")
	default:
		delay = nakDelay(meta.NumDelivered)
	}
	if err := msg.NakWithDelay(delay); err != nil {
		logger.Warn().Err(err).Msg("could not nak tenant event")
	}
}

// nakDelay returns the redelivery delay after the numDelivered-th
// delivery failed, clamped to the last redeliverBackoff entry. It mirrors
// ConsumerConfig.nakDelay of the workflow service's signal router
// (backend/workflow/services), which lives in another module and is a
// method on that router's own config.
//
// The count is compared, never converted: it is not bounded by MaxDeliver
// when the consumer has none (nak treats a non-positive one as
// unlimited), and converting it to int wraps every count above MaxInt64
// to a negative index, which a clamp would read as the first delay.
func nakDelay(numDelivered uint64) time.Duration {
	delay := redeliverBackoff[0]
	var delivery uint64 = 1
	for _, d := range redeliverBackoff {
		if delivery > numDelivered {
			break
		}
		delay = d
		delivery++
	}
	return delay
}
