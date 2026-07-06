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

// driftResult is the pure (DB- and Zitadel-free) output of the drift
// classification. toDisable is fully resolved because its tenant_ids come
// from disabledByOrg; restoreOrgIDs is returned as bare org IDs because
// those rows are active and their tenant_ids must still be looked up.
// deletedDisabled carries DB-disabled tenants whose org no longer exists
// — terminal, not drift — surfaced only for logging.
type driftResult struct {
	toDisable       []TenantRef
	restoreOrgIDs   []string
	deletedDisabled []TenantRef
}

// computeDrift is the pure decision-matrix core of ComputeDriftActivity
// (see that method's doc comment for the matrix). It performs no I/O so
// it can be unit-tested exhaustively. Inputs:
//
//   - disabledByOrg:  org_id → tenant_id for DB rows with deleted_at set.
//   - activeOrgIDs:   org_ids in Zitadel state ACTIVE.
//   - inactiveOrgIDs: org_ids in Zitadel state INACTIVE.
//
// An org_id present in neither active nor inactive is treated as deleted.
func computeDrift(disabledByOrg map[string]uuid.UUID, activeOrgIDs, inactiveOrgIDs map[string]struct{}) driftResult {
	var res driftResult
	for orgID, tenantID := range disabledByOrg {
		if _, active := activeOrgIDs[orgID]; active {
			// DB disabled, Zitadel still ACTIVE → need disable.
			res.toDisable = append(res.toDisable, TenantRef{TenantID: tenantID, IdpOrgRef: orgID})
			continue
		}
		if _, inactive := inactiveOrgIDs[orgID]; !inactive {
			// Neither active nor inactive → deleted in Zitadel. DB row is
			// already disabled, so the states are aligned: terminal.
			res.deletedDisabled = append(res.deletedDisabled, TenantRef{TenantID: tenantID, IdpOrgRef: orgID})
		}
		// else: DB disabled, Zitadel INACTIVE → already aligned, no-op.
	}
	for orgID := range inactiveOrgIDs {
		if _, disabled := disabledByOrg[orgID]; !disabled {
			// Zitadel inactive, DB not disabled → need restore.
			res.restoreOrgIDs = append(res.restoreOrgIDs, orgID)
		}
	}
	return res
}

// ComputeDriftActivity returns the set of tenants whose DB deleted_at
// disagrees with their Zitadel org state. A deleted org is absent from
// ListOrganizations entirely, so it is a distinct third state from
// active/inactive.
//
// Decision matrix (DB row × Zitadel org state):
//
//	DB \ org   | ACTIVE      | INACTIVE   | DELETED
//	-----------+-------------+------------+----------------------------
//	disabled   | ToDisable   | aligned    | aligned (skip+log)
//	active     | aligned     | ToRestore  | out of scope → zitadel-sync
//
// DELETED + disabled is already aligned (org gone, DB disabled), so it is
// NOT drift — emitting a disable would loop forever, since the org can
// never become "inactive" and each dispatch hits NotFound. DELETED +
// active is left to zitadel-sync's ReconcileTenantsActivity, which
// soft-deletes DB tenants missing from Zitadel (the SSOT).
//
// Cost: the drift sets themselves stay tiny in steady state (usually
// empty), but the Zitadel org listing is O(total orgs) per run — it
// enumerates every org with no state filter so a DELETED org can be told
// apart from ACTIVE/INACTIVE. A small DB query fills in tenant_ids for the
// ToRestore side since those rows are not in the disabled set.
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

	// All Zitadel orgs, bucketed by state in a single paginated pass.
	// Querying without a state filter returns every org with its State
	// field set, so we can separate ACTIVE from INACTIVE here and still
	// tell both apart from an org that is absent entirely (deleted in
	// Zitadel). Paginated — without an explicit ListQuery, Zitadel applies
	// its default page cap and silently truncates, hiding drift past the
	// first page.
	orgClient := org_pb.NewOrganizationServiceClient(a.zitadelConn)

	allOrgs, err := paginateOrgs(ctx, orgClient, nil)
	if err != nil {
		logger.Error("ListOrganizations failed", "err", err)
		return DriftSet{}, fmt.Errorf("list zitadel orgs: %w", err)
	}

	activeOrgIDs := make(map[string]struct{})
	inactiveOrgIDs := make(map[string]struct{})
	for _, o := range allOrgs {
		switch o.GetState() {
		case org_pb.OrganizationState_ORGANIZATION_STATE_ACTIVE:
			activeOrgIDs[o.GetId()] = struct{}{}
		case org_pb.OrganizationState_ORGANIZATION_STATE_INACTIVE:
			inactiveOrgIDs[o.GetId()] = struct{}{}
		}
	}

	// Apply the pure decision matrix (no I/O, unit-tested in
	// activities_test.go).
	res := computeDrift(disabledByOrg, activeOrgIDs, inactiveOrgIDs)
	drift := DriftSet{ToDisable: res.toDisable}

	// Deleted-org + DB-disabled is terminal, not drift. Log so an operator
	// can see leftover orgs being skipped rather than retried.
	for _, ref := range res.deletedDisabled {
		logger.Info("DB-disabled tenant has no Zitadel org; treating as consistent",
			"tenant_id", ref.TenantID, "idp_org_ref", ref.IdpOrgRef)
	}

	// Resolve tenant_ids for the restore side via a single batched query.
	// Tenants in res.restoreOrgIDs are active (not in the disabled set), so
	// we look them up by idp_org_ref. DeletedAtIsNil is required even under
	// FEATURE_SHOW_DELETED: a concurrent soft-delete between the disabled
	// snapshot above and this query would otherwise leak through to
	// ToRestore and override the admin's intent.
	if len(res.restoreOrgIDs) > 0 {
		rows, err := a.ent.Tenant.Query().
			Where(
				enttenant.IdpOrgRefIn(res.restoreOrgIDs...),
				enttenant.DeletedAtIsNil(),
			).
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
