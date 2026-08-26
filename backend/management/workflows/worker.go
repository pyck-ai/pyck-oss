package workflows

import (
	"context"
	"time"

	zitadelsdk "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/exec"
	"github.com/pyck-ai/pyck/backend/management/workerapi"
	disabletenant "github.com/pyck-ai/pyck/backend/management/workflows/disable-tenant"
	generatejsonschema "github.com/pyck-ai/pyck/backend/management/workflows/generate-json-schema"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
	restoretenant "github.com/pyck-ai/pyck/backend/management/workflows/restore-tenant"
	tenantexpirycheck "github.com/pyck-ai/pyck/backend/management/workflows/tenant-expiry-check"
	tenantreconcile "github.com/pyck-ai/pyck/backend/management/workflows/tenant-reconcile"
	zitadelsync "github.com/pyck-ai/pyck/backend/management/workflows/zitadel-sync"
)

const (
	TemporalManagementTaskQueue = "pyck-management-task-queue"

	RegisterTenantWorkflow     = "RegisterTenantWorkflow"
	GenerateJsonSchemaWorkflow = "GenerateJsonSchemaWorkflow"
	ZitadelSyncWorkflow        = "ZitadelSyncWorkflow"
	TenantSyncWorkflow         = "TenantSyncWorkflow"
	DisableTenantWorkflow      = "DisableTenantWorkflow"
	RestoreTenantWorkflow      = "RestoreTenantWorkflow"
	TenantReconcileWorkflow    = "TenantReconcileWorkflow"
	TenantExpiryCheckWorkflow  = "TenantExpiryCheckWorkflow"
)

type TemporalWorker struct {
	temporalWorker   worker.Worker
	tenantSyncWorker worker.Worker
	taskQueueName    string
	resolver         exec.MutationResolver
	cli              client.Client
	versioning       commonworkflow.VersioningConfig
	depOpts          worker.DeploymentOptions
}

type WorkerOptions struct {
	EnableTenantSync bool
	// Versioning registers both workers under one Temporal deployment version
	// (#1132). Zero value keeps them unversioned.
	Versioning commonworkflow.VersioningConfig
}

func NewTemporalWorker(cli client.Client, taskQueue string, resolver exec.MutationResolver, opts WorkerOptions) (*TemporalWorker, error) {
	main, sync, err := workerOptions(opts.Versioning)
	if err != nil {
		return nil, err
	}

	var tenantSync worker.Worker
	if opts.EnableTenantSync {
		tenantSync = worker.New(cli, zitadelsync.TenantSyncTaskQueue, sync)
	}

	return &TemporalWorker{
		temporalWorker:   worker.New(cli, taskQueue, main),
		tenantSyncWorker: tenantSync,
		taskQueueName:    taskQueue,
		resolver:         resolver,
		cli:              cli,
		versioning:       opts.Versioning,
		depOpts:          main.DeploymentOptions,
	}, nil
}

// PromoteVersion makes this worker's version current, for deployments the
// temporal-worker-controller does not manage.
func (tw *TemporalWorker) PromoteVersion(ctx context.Context) {
	tw.versioning.StartPromotion(ctx, tw.cli, tw.depOpts)
}

// workerOptions builds the options for both task queues. They share one
// deployment version: they are one deploy of one binary, and versioning them
// separately would let a rollout promote half of it.
func workerOptions(versioning commonworkflow.VersioningConfig) (main, sync worker.Options, err error) {
	// Unversioned resolves to the zero DeploymentOptions, which is already
	// "no versioning" — no branch needed.
	depOpts, _, err := versioning.DeploymentOptionsFromBuild()
	if err != nil {
		return worker.Options{}, worker.Options{}, err
	}

	main = worker.Options{DeploymentOptions: depOpts}
	sync = worker.Options{
		DeploymentOptions:                  depOpts,
		TaskQueueActivitiesPerSecond:       100,
		MaxConcurrentActivityExecutionSize: 200,
		MaxConcurrentActivityTaskPollers:   4,
	}

	return main, sync, nil
}

// Versioned reports whether the workers registered a deployment version — an
// unversioned worker in a deployed environment is otherwise invisible.
func (tw *TemporalWorker) Versioned() bool { return tw.depOpts.UseVersioning }

func (tw *TemporalWorker) Start() error {
	// Start tenant-sync first so it's ready for scheduled runs.
	if tw.tenantSyncWorker != nil {
		if err := tw.tenantSyncWorker.Start(); err != nil {
			return err
		}
	}

	return tw.temporalWorker.Start()
}

func (tw *TemporalWorker) Stop() {
	tw.temporalWorker.Stop()
	if tw.tenantSyncWorker != nil {
		tw.tenantSyncWorker.Stop()
	}
}

func (tw *TemporalWorker) Run() error {
	if tw.tenantSyncWorker == nil {
		return tw.temporalWorker.Run(worker.InterruptCh())
	}

	errCh := make(chan error, 2)
	go func() { errCh <- tw.tenantSyncWorker.Run(worker.InterruptCh()) }()
	go func() { errCh <- tw.temporalWorker.Run(worker.InterruptCh()) }()
	return <-errCh
}

func (tw *TemporalWorker) RegisterTenantWorkflow(entClient *gen.Client, nsGetter commonworkflow.NamespaceGetter, workerAPI *workerapi.Client) {
	// Register workflows and activities
	registerTenantWorkflowOptions := workflow.RegisterOptions{
		Name: RegisterTenantWorkflow,
	}

	tw.temporalWorker.RegisterWorkflowWithOptions(registertenant.RegisterTenantWorkflow, registerTenantWorkflowOptions)
	tw.temporalWorker.RegisterActivity(registertenant.NewActivities(tw.resolver, entClient, tw.cli, nsGetter, workerAPI))
}

func (tw *TemporalWorker) RegisterGenerateJsonSchemaWorkflow() {
	generateJsonSchemaWorkflowOptions := workflow.RegisterOptions{
		Name: GenerateJsonSchemaWorkflow,
	}

	tw.temporalWorker.RegisterWorkflowWithOptions(generatejsonschema.GenerateJSONSchemaWorkflow, generateJsonSchemaWorkflowOptions)
	tw.temporalWorker.RegisterActivity(generatejsonschema.GenerateJSONSchemaActivity)
}

// RegisterZitadelSyncWorkflow registers the Zitadel sync orchestrator, per-tenant sync workflow,
// and all related activities using the Activities object (no closures).
func (tw *TemporalWorker) RegisterZitadelSyncWorkflow(entClient *gen.Client, apiURL, grpcAddr, audience, keyFilePath, zitadelProjectID string, tlsInsecure bool) {
	// Orchestrator workflow (runs on management worker)
	tw.temporalWorker.RegisterWorkflowWithOptions(
		zitadelsync.ZitadelSyncWorkflow,
		workflow.RegisterOptions{Name: ZitadelSyncWorkflow},
	)

	// Activities object with DI for both workers
	acts := zitadelsync.NewActivities(entClient, tw.cli, apiURL, grpcAddr, audience, keyFilePath, zitadelProjectID, tlsInsecure)

	// Orchestrator activities (tenants + schedules) — keep names stable for determinism
	tw.temporalWorker.RegisterActivityWithOptions(
		acts.FetchZitadelTenantsActivity,
		activity.RegisterOptions{Name: "FetchZitadelTenantsActivity"},
	)
	tw.temporalWorker.RegisterActivityWithOptions(
		acts.FetchDbTenantsActivity,
		activity.RegisterOptions{Name: "FetchDbTenantsActivity"},
	)
	tw.temporalWorker.RegisterActivityWithOptions(
		acts.ReconcileTenantsActivity,
		activity.RegisterOptions{Name: "ReconcileTenantsActivity"},
	)
	tw.temporalWorker.RegisterActivityWithOptions(
		acts.StartTenantSyncActivity,
		activity.RegisterOptions{Name: "StartTenantSyncActivity"},
	)

	// Per-tenant sync workflow (runs on dedicated tenant-sync worker)
	if tw.tenantSyncWorker != nil {
		tw.tenantSyncWorker.RegisterWorkflowWithOptions(
			zitadelsync.TenantSyncWorkflow,
			workflow.RegisterOptions{Name: TenantSyncWorkflow},
		)
		tw.tenantSyncWorker.RegisterActivityWithOptions(
			acts.FetchZitadelUsersActivity,
			activity.RegisterOptions{Name: "FetchZitadelUsersActivity"},
		)
		tw.tenantSyncWorker.RegisterActivityWithOptions(
			acts.FetchDbUsersActivity,
			activity.RegisterOptions{Name: "FetchDbUsersActivity"},
		)
		tw.tenantSyncWorker.RegisterActivityWithOptions(
			acts.ReconcileUsersActivity,
			activity.RegisterOptions{Name: "ReconcileUsersActivity"},
		)
	}
}

// EnsureZitadelSyncOrchestratorSchedule ensures/updates the top-level schedule that spawns per-tenant schedules.
func (tw *TemporalWorker) EnsureZitadelSyncOrchestratorSchedule(ctx context.Context, temporalClient client.Client, every time.Duration) error {
	return zitadelsync.EnsureOrchestratorSchedule(ctx, temporalClient, tw.taskQueueName, every)
}

// RegisterDisableTenantWorkflow registers the DisableTenantWorkflow and
// the shared disable-tenant activities on the management task queue.
// The workflow is triggered externally by the tenant-lifecycle NATS
// subscriber after the resolver has already updated deleted_at + state.
//
// Must be called before RegisterRestoreTenantWorkflow because the shared
// Activities struct is registered here — calling RegisterActivity twice
// with the same struct would cause Temporal to panic on duplicate
// registration.
func (tw *TemporalWorker) RegisterDisableTenantWorkflow(zitadelConn *zitadelsdk.Connection) {
	tw.temporalWorker.RegisterWorkflowWithOptions(
		disabletenant.DisableTenantWorkflow,
		workflow.RegisterOptions{Name: DisableTenantWorkflow},
	)
	tw.temporalWorker.RegisterActivity(disabletenant.NewActivities(zitadelConn))
}

// RegisterRestoreTenantWorkflow registers the RestoreTenantWorkflow and
// its activities on the management task queue.
func (tw *TemporalWorker) RegisterRestoreTenantWorkflow(zitadelConn *zitadelsdk.Connection) {
	tw.temporalWorker.RegisterWorkflowWithOptions(
		restoretenant.RestoreTenantWorkflow,
		workflow.RegisterOptions{Name: RestoreTenantWorkflow},
	)
	tw.temporalWorker.RegisterActivity(restoretenant.NewActivities(zitadelConn))
}

// RegisterTenantReconcileWorkflow registers the tenant reconcile
// orchestrator workflow and its activities on the management task
// queue. The reconciler sweeps tenants and dispatches corrective
// disable/restore workflows when the DB's deleted_at disagrees with
// Zitadel's org state — it is the eventual-consistency safety net for
// the NATS-driven trigger (see events/tenants/trigger.go).
func (tw *TemporalWorker) RegisterTenantReconcileWorkflow(entClient *gen.Client, zitadelConn *zitadelsdk.Connection) {
	tw.temporalWorker.RegisterWorkflowWithOptions(
		tenantreconcile.TenantReconcileWorkflow,
		workflow.RegisterOptions{Name: TenantReconcileWorkflow},
	)

	acts := tenantreconcile.NewActivities(entClient, tw.cli, zitadelConn, tenantreconcile.Config{
		TaskQueue:           tw.taskQueueName,
		DisableWorkflowName: DisableTenantWorkflow,
		RestoreWorkflowName: RestoreTenantWorkflow,
		LifecycleWorkflowID: TenantLifecycleWorkflowID,
	})

	// Register the whole struct so each method's default reflected name
	// is used (FetchTenantsActivity, ReconcileTenantActivity). The
	// workflow references these via method values on a nil receiver,
	// which gives us compile-time checking instead of string matching.
	tw.temporalWorker.RegisterActivity(acts)
}

// EnsureTenantReconcileSchedule creates/updates the reconcile sweeper's
// Temporal schedule. The `every` duration must exceed the NATS
// redelivery window (see maxRedeliver in events/tenants/config.go) so
// the sweeper only runs after a stuck lifecycle would have dropped.
func (tw *TemporalWorker) EnsureTenantReconcileSchedule(ctx context.Context, temporalClient client.Client, every time.Duration) error {
	return tenantreconcile.EnsureSchedule(ctx, temporalClient, tw.taskQueueName, every)
}

// RegisterTenantExpiryCheckWorkflow registers the periodic
// tenant-expiry-check workflow on the management task queue. The
// workflow soft-deletes tenants whose expires_at is in the past by
// going through the same Ent + outbox + NATS path as the
// disableTenant resolver — the existing tenant-lifecycle subscriber
// then picks it up and starts DisableTenantWorkflow.
func (tw *TemporalWorker) RegisterTenantExpiryCheckWorkflow(entClient *gen.Client) {
	tw.temporalWorker.RegisterWorkflowWithOptions(
		tenantexpirycheck.TenantExpiryCheckWorkflow,
		workflow.RegisterOptions{Name: TenantExpiryCheckWorkflow},
	)
	tw.temporalWorker.RegisterActivity(tenantexpirycheck.NewActivities(entClient))
}

// EnsureTenantExpiryCheckSchedule creates/updates the expiry-check
// schedule. Unlike the reconcile sweeper, the cadence is bounded only
// by how soon you want expired tenants to be disabled — there's no
// NATS-redelivery interaction to avoid.
func (tw *TemporalWorker) EnsureTenantExpiryCheckSchedule(ctx context.Context, temporalClient client.Client, every time.Duration) error {
	return tenantexpirycheck.EnsureSchedule(ctx, temporalClient, tw.taskQueueName, every)
}
