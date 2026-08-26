// Package tenants subscribes to management's tenant CRUD events and
// dispatches the corresponding tenant lifecycle workflow.
//
// Trigger flow:
//  1. Resolver does tx.Tenant.Update(SetDeletedAt | ClearDeletedAt)
//  2. Outbox hook emits pyck.<tenant>.crud.management.tenant.<id>.updated
//  3. SubscribeTrigger receives the event
//  4. Compares deleted_at before/after and starts DisableTenantWorkflow
//     or RestoreTenantWorkflow
package tenants

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"go.temporal.io/api/enums/v1"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/events/topic"
	"github.com/pyck-ai/pyck/backend/common/log"

	"github.com/pyck-ai/pyck/backend/management/workflows"
	disabletenant "github.com/pyck-ai/pyck/backend/management/workflows/disable-tenant"
	restoretenant "github.com/pyck-ai/pyck/backend/management/workflows/restore-tenant"
)

// SubscribeTrigger creates a JetStream consumer for tenant CRUD update
// events and starts the appropriate lifecycle workflow (disable or restore)
// based on the observed state transition.
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
) (jetstream.ConsumeContext, error) {
	// Subscribe to both "updated" and "deleted" operations because the
	// outbox hook reclassifies SetDeletedAt mutations as soft deletes
	// (operation="deleted"). Disable triggers "deleted", restore triggers
	// "updated".
	subjects := []string{
		events.MutationEventTopic{
			StreamName:    streamName,
			ServiceName:   topic.ManagementService,
			SchemaName:    topic.TenantSchema,
			OperationName: events.OpUpdate,
		}.String(),
		events.MutationEventTopic{
			StreamName:    streamName,
			ServiceName:   topic.ManagementService,
			SchemaName:    topic.TenantSchema,
			OperationName: events.OpDelete,
		}.String(),
	}

	log.ForContext(ctx).Info().
		Strs("subjects", subjects).
		Msg("subscribing to tenant lifecycle trigger events")

	consumer, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Name:           "management-tenant-lifecycle-trigger",
		Durable:        "management-tenant-lifecycle-trigger",
		FilterSubjects: subjects,
		AckPolicy:      jetstream.AckExplicitPolicy,
		MaxDeliver:     maxRedeliver,
		BackOff:        redeliverBackoff,
	})
	if err != nil {
		return nil, fmt.Errorf("create trigger consumer: %w", err)
	}

	cc, err := consumer.Consume(func(msg jetstream.Msg) {
		handleTriggerMessage(ctx, msg, temporalClient)
	})
	if err != nil {
		return nil, fmt.Errorf("consume trigger events: %w", err)
	}

	return cc, nil
}

// handleTriggerMessage processes a single tenant update event.
func handleTriggerMessage(
	ctx context.Context,
	msg jetstream.Msg,
	temporalClient temporalclient.Client,
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

	logger.Info().Msg("received tenant mutation event")

	if err := json.Unmarshal(msg.Data(), &event); err != nil {
		logger.Error().Err(err).Msg("decode mutation event")
		_ = msg.Ack()
		return
	}

	// Sanity-check the event matches what we expect to handle.
	// The outbox hook emits "deleted" for disable (soft delete) and
	// "updated" for restore (clearing deleted_at).
	if event.Service != topic.ManagementService || !strings.EqualFold(event.Schema, topic.TenantSchema) {
		_ = msg.Ack()
		return
	}
	if event.Operation != events.OpUpdate && event.Operation != events.OpDelete {
		_ = msg.Ack()
		return
	}

	dataAfter, ok := event.DataAfter.(map[string]any)
	if !ok {
		logger.Debug().Msg("DataAfter is not a map, skipping")
		_ = msg.Ack()
		return
	}

	dataBefore, ok := event.DataBefore.(map[string]any)
	if !ok {
		logger.Debug().Msg("DataBefore is not a map, skipping")
		_ = msg.Ack()
		return
	}

	tenantID := event.ID
	if tenantID == uuid.Nil {
		logger.Error().Msg("event has nil tenant ID; cannot dispatch workflow")
		_ = msg.Ack()
		return
	}

	idpOrgRef, ok := dataAfter["idp_org_ref"].(string)
	if !ok || idpOrgRef == "" {
		logger.Error().Msg("event missing idp_org_ref; cannot dispatch workflow")
		_ = msg.Ack()
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
		_ = msg.Ack()
		return
	}

	// Shared workflow ID serializes disable and restore per tenant.
	// TERMINATE_EXISTING enforces "latest user intent wins": an
	// opposite-direction event kills the in-flight workflow and
	// runs the new one in its place. Safe because both Zitadel
	// activities are idempotent (deactivate INACTIVE / activate
	// ACTIVE are no-ops) and JetStream per-subject ordering
	// guarantees events arrive in user-intent order.
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

	run, err := temporalClient.ExecuteWorkflow(ctx, opts, workflowName, input)
	if err != nil {
		// Real failures (Temporal unreachable, malformed input, etc.)
		// NAK so JetStream redelivers; tenant-reconcile is the
		// longer-window safety net.
		logger.Error().Err(err).Msg("failed to start lifecycle workflow")
		_ = msg.Nak()
		return
	}

	logger.Info().
		Str("run_id", run.GetRunID()).
		Msg("lifecycle workflow started")

	_ = msg.Ack()
}
