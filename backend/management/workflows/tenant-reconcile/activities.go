package tenantreconcile

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	zitadelsdk "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	object_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	org_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/feature"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	disabletenant "github.com/pyck-ai/pyck/backend/management/workflows/disable-tenant"
	restoretenant "github.com/pyck-ai/pyck/backend/management/workflows/restore-tenant"
)

type (
	// Activities groups the side-effect activities for the tenant reconcile
	// workflow. Dependencies are injected via NewActivities; no globals.
	//
	// Workflow identifiers (task queue, disable/restore workflow names,
	// shared lifecycle workflow ID) are passed in as DI fields so this
	// package does not have to import backend/management/workflows or
	// backend/management/events/tenants, which would create an import
	// cycle with worker.go.
	Activities struct {
		ent         *ent.Client
		temporal    temporalclient.Client
		zitadelConn *zitadelsdk.Connection
		cfg         Config
	}

	// Config carries the workflow identifiers the reconciler needs to
	// dispatch corrective workflows. Wired from main.go via
	// workflows.RegisterTenantReconcileWorkflow.
	Config struct {
		// TaskQueue is the management task queue where disable and restore
		// workflows are dispatched.
		TaskQueue string

		// DisableWorkflowName / RestoreWorkflowName are the Temporal workflow
		// type names registered on the worker.
		DisableWorkflowName string
		RestoreWorkflowName string

		// LifecycleWorkflowID returns the shared workflow ID for the
		// tenant's disable/restore lifecycle — same ID used by the NATS
		// trigger so the conflict policy serializes against it.
		LifecycleWorkflowID func(tenantID uuid.UUID) string
	}
)

// ErrUnknownReconcileOp is returned by DispatchLifecycleActivity when
// the input carries an op that is neither OpDisable nor OpRestore.
var ErrUnknownReconcileOp = errors.New("unknown reconcile op")

// activityRefs is a nil-receiver Activities used only so the workflow
// can pass type-safe method references to workflow.ExecuteActivity
// instead of hard-coded name strings. Temporal dispatches by the
// reflected method name to whichever real receiver was registered on
// the worker; the nil receiver here is never invoked.
var activityRefs *Activities

// NewActivities wires the reconcile activities with the required clients.
// The Temporal client is needed to dispatch corrective disable/restore
// workflows. The Zitadel gRPC connection is used to read org state via
// the admin API (same pattern as disable-tenant/activities.go).
func NewActivities(entClient *ent.Client, temporal temporalclient.Client, zitadelConn *zitadelsdk.Connection, cfg Config) *Activities {
	return &Activities{
		ent:         entClient,
		temporal:    temporal,
		zitadelConn: zitadelConn,
		cfg:         cfg,
	}
}

// ComputeDriftActivity returns the set of tenants whose DB deleted_at
// disagrees with their Zitadel org state. It does so with two narrow
// lookups instead of scanning every tenant:
//
//   - DB tenants with deleted_at NOT NULL → set D (idp_org_ref → tenant_id).
//   - Zitadel orgs in state INACTIVE      → set I (org_id).
//
// Drift is the symmetric difference:
//
//	ToDisable = D \ I   (DB disabled, Zitadel still active)
//	ToRestore = I \ D   (Zitadel inactive, DB says active)
//
// In steady state both sets are tiny (usually empty), so the cost is
// O(drift), not O(tenants). A third small DB query fills in tenant_ids
// for the ToRestore side since those rows are not in D.
func (a *Activities) ComputeDriftActivity(ctx context.Context, _ ComputeDriftActivityInput) (DriftSet, error) {
	logger := activity.GetLogger(ctx)
	sysCtx := authn.Context(ctx, authn.SystemUser())
	sysCtx = feature.Context(sysCtx, feature.FEATURE_SHOW_DELETED)

	// DB disabled tenants — idp_org_ref → tenant_id.
	// Exclude rows without an idp_org_ref at the DB level: they can't
	// map to a Zitadel org, so they're irrelevant for drift detection.
	disabledRows, err := a.ent.Tenant.Query().
		Where(
			enttenant.DeletedAtNotNil(),
			enttenant.IdpOrgRefNEQ(""),
		).
		AllPages(sysCtx, dbPageSize)
	if err != nil {
		logger.Error("failed to list disabled tenants", "err", err)
		return DriftSet{}, fmt.Errorf("list disabled tenants: %w", err)
	}

	disabledByOrg := make(map[string]uuid.UUID, len(disabledRows))
	for _, t := range disabledRows {
		disabledByOrg[t.IdpOrgRef] = t.ID
	}

	// Zitadel orgs in INACTIVE state. Paginated — without an explicit
	// ListQuery, Zitadel applies its default page cap and silently
	// truncates, hiding drift past the first page.
	orgClient := org_pb.NewOrganizationServiceClient(a.zitadelConn)
	inactiveOrgs, err := paginateOrgs(ctx, orgClient, []*org_pb.SearchQuery{{
		Query: &org_pb.SearchQuery_StateQuery{
			StateQuery: &org_pb.OrganizationStateQuery{
				State: org_pb.OrganizationState_ORGANIZATION_STATE_INACTIVE,
			},
		},
	}})
	if err != nil {
		logger.Error("ListOrganizations (inactive) failed", "err", err)
		return DriftSet{}, fmt.Errorf("list inactive zitadel orgs: %w", err)
	}
	inactiveOrgIDs := make(map[string]struct{}, len(inactiveOrgs))
	for _, o := range inactiveOrgs {
		inactiveOrgIDs[o.GetId()] = struct{}{}
	}

	// Compute drift sets.
	drift := DriftSet{}
	var restoreOrgIDs []string
	for orgID, tenantID := range disabledByOrg {
		if _, inactive := inactiveOrgIDs[orgID]; !inactive {
			// DB disabled, Zitadel not inactive → need disable.
			drift.ToDisable = append(drift.ToDisable, TenantRef{TenantID: tenantID, IdpOrgRef: orgID})
		}
	}
	for orgID := range inactiveOrgIDs {
		if _, disabled := disabledByOrg[orgID]; !disabled {
			// Zitadel inactive, DB not disabled → need restore.
			// Collect org_ids for a single batched tenant lookup below.
			restoreOrgIDs = append(restoreOrgIDs, orgID)
		}
	}

	// Resolve tenant_ids for the restore side via a single batched query.
	// Tenants in restoreOrgIDs are active (not in set D), so we look them
	// up by idp_org_ref.
	if len(restoreOrgIDs) > 0 {
		rows, err := a.ent.Tenant.Query().
			Where(enttenant.IdpOrgRefIn(restoreOrgIDs...)).
			AllPages(sysCtx, mixin.Limit)
		if err != nil {
			logger.Error("failed to resolve tenants for restore set", "err", err)
			return DriftSet{}, fmt.Errorf("resolve restore tenants: %w", err)
		}
		for _, t := range rows {
			drift.ToRestore = append(drift.ToRestore, TenantRef{TenantID: t.ID, IdpOrgRef: t.IdpOrgRef})
		}
	}

	logger.Info("drift computed",
		"db_disabled", len(disabledByOrg),
		"zitadel_inactive", len(inactiveOrgIDs),
		"to_disable", len(drift.ToDisable),
		"to_restore", len(drift.ToRestore),
	)
	return drift, nil
}

// DispatchLifecycleActivity starts the corrective disable or restore
// workflow on the shared tenant-lifecycle workflow ID. If a lifecycle
// workflow is already running for the tenant, the dispatch is deferred
// — the running workflow will converge the state and the next sweep
// will retry if it didn't.
func (a *Activities) DispatchLifecycleActivity(ctx context.Context, input DispatchLifecycleActivityInput) (DispatchLifecycleActivityOutput, error) {
	logger := activity.GetLogger(ctx)

	workflowID := a.cfg.LifecycleWorkflowID(input.TenantID)
	opts := temporalclient.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                a.cfg.TaskQueue,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
	}

	var (
		workflowName string
		workflowArg  any
	)
	switch input.Op {
	case OpDisable:
		workflowName = a.cfg.DisableWorkflowName
		workflowArg = disabletenant.DisableTenantWorkflowInput{
			TenantID:  input.TenantID,
			IdpOrgRef: input.IdpOrgRef,
		}
	case OpRestore:
		workflowName = a.cfg.RestoreWorkflowName
		workflowArg = restoretenant.RestoreTenantWorkflowInput{
			TenantID:  input.TenantID,
			IdpOrgRef: input.IdpOrgRef,
		}
	default:
		return DispatchLifecycleActivityOutput{}, fmt.Errorf("%w: %q", ErrUnknownReconcileOp, input.Op)
	}

	logger.Info("dispatching corrective lifecycle workflow",
		"tenant_id", input.TenantID,
		"idp_org_ref", input.IdpOrgRef,
		"op", input.Op,
		"workflow", workflowName,
	)

	if _, err := a.temporal.ExecuteWorkflow(ctx, opts, workflowName, workflowArg); err != nil {
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &already) {
			logger.Info("lifecycle workflow already running; deferring",
				"tenant_id", input.TenantID)
			return DispatchLifecycleActivityOutput{Dispatched: false, Deferred: true}, nil
		}
		return DispatchLifecycleActivityOutput{}, fmt.Errorf("start corrective workflow: %w", err)
	}

	return DispatchLifecycleActivityOutput{Dispatched: true}, nil
}

const (
	// dbPageSize must stay at or below mixin.Limit (the LimitMixin cap,
	// 200 at time of writing) — exceeding it returns ErrLimitExceeded
	// from the LimitInterceptor.
	dbPageSize             = 100
	zitadelPageSize uint32 = 1000
)

// paginateOrgs iterates ListOrganizations until Zitadel returns a short
// page. Without an explicit ListQuery the server applies its default
// page cap and silently truncates the response.
func paginateOrgs(ctx context.Context, client org_pb.OrganizationServiceClient, queries []*org_pb.SearchQuery) ([]*org_pb.Organization, error) {
	var all []*org_pb.Organization
	var offset uint64
	for {
		resp, err := client.ListOrganizations(ctx, &org_pb.ListOrganizationsRequest{
			Query:   &object_pb.ListQuery{Offset: offset, Limit: zitadelPageSize},
			Queries: queries,
		})
		if err != nil {
			return nil, err
		}
		page := resp.GetResult()
		all = append(all, page...)
		if len(page) < int(zitadelPageSize) {
			break
		}
		offset += uint64(zitadelPageSize)
	}
	return all, nil
}
