package workflows

import (
	"context"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/management"
	"github.com/pyck-ai/pyck/backend/management/ent/gen"
	generatejsonschema "github.com/pyck-ai/pyck/backend/management/workflows/generate-json-schema"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
	temporalsetup "github.com/pyck-ai/pyck/backend/management/workflows/temporal-setup"
	zitadelsetup "github.com/pyck-ai/pyck/backend/management/workflows/zitadel-setup"
	zitadelsync "github.com/pyck-ai/pyck/backend/management/workflows/zitadel-sync"
)

const (
	TemporalBootstrapTaskQueue  = "pyck-bootstrap-task-queue"
	TemporalManagementTaskQueue = "pyck-management-task-queue"

	RegisterTenantWorkflow     = "RegisterTenantWorkflow"
	ZitadelSetupWorkflow       = "ZitadelSetupWorkflow"
	TemporalSetupWorkflow      = "TemporalSetupWorkflow"
	GenerateJsonSchemaWorkflow = "GenerateJsonSchemaWorkflow"
	ZitadelSyncWorkflow        = "ZitadelSyncWorkflow"
	TenantSyncWorkflow         = "TenantSyncWorkflow"
)

type TemporalWorker struct {
	temporalWorker   worker.Worker
	tenantSyncWorker worker.Worker
	taskQueueName    string
	resolver         management.MutationResolver
	cli              client.Client
}

type WorkerOptions struct {
	EnableTenantSync bool
}

func NewTemporalWorker(cli client.Client, taskQueue string, resolver management.MutationResolver, opts WorkerOptions) (*TemporalWorker, error) {
	var tenantSync worker.Worker
	if opts.EnableTenantSync {
		tenantSync = worker.New(cli, zitadelsync.TenantSyncTaskQueue, worker.Options{
			TaskQueueActivitiesPerSecond:       100,
			MaxConcurrentActivityExecutionSize: 200,
			MaxConcurrentActivityTaskPollers:   4,
		})
	}

	return &TemporalWorker{
		temporalWorker:   worker.New(cli, taskQueue, worker.Options{}),
		tenantSyncWorker: tenantSync,
		taskQueueName:    taskQueue,
		resolver:         resolver,
		cli:              cli,
	}, nil
}

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

func (tw *TemporalWorker) RegisterTenantWorkflow(entClient *gen.Client, nsGetter commonworkflow.NamespaceGetter) {
	// Register workflows and activities
	registerTenantWorkflowOptions := workflow.RegisterOptions{
		Name: RegisterTenantWorkflow,
	}

	tw.temporalWorker.RegisterWorkflowWithOptions(registertenant.RegisterTenantWorkflow, registerTenantWorkflowOptions)
	tw.temporalWorker.RegisterActivity(registertenant.NewActivities(tw.resolver, entClient, tw.cli, nsGetter))
}

func (tw *TemporalWorker) RegisterZitadelSetupWorkflow() {
	zitadelSetupWorkflowOptions := workflow.RegisterOptions{
		Name: ZitadelSetupWorkflow,
	}
	tw.temporalWorker.RegisterWorkflowWithOptions(zitadelsetup.ZitadelSetupWorkflow, zitadelSetupWorkflowOptions)
	tw.temporalWorker.RegisterActivity(zitadelsetup.WaitForHostReachable)
	tw.temporalWorker.RegisterActivity(zitadelsetup.GetOrgID)
	tw.temporalWorker.RegisterActivity(zitadelsetup.AddOrgDomainSuffixPattern)
	tw.temporalWorker.RegisterActivity(zitadelsetup.CreateUser)
	tw.temporalWorker.RegisterActivity(zitadelsetup.SetUserAsAdmin)
	tw.temporalWorker.RegisterActivity(zitadelsetup.AddProject)
	tw.temporalWorker.RegisterActivity(zitadelsetup.AddProjectRoles)
	tw.temporalWorker.RegisterActivity(zitadelsetup.AddAppToProject)
	tw.temporalWorker.RegisterActivity(zitadelsetup.AddJsonAppKey)
	tw.temporalWorker.RegisterActivity(zitadelsetup.AddServiceUser)
	tw.temporalWorker.RegisterActivity(zitadelsetup.AddServiceUserToken)
	tw.temporalWorker.RegisterActivity(zitadelsetup.AddServiceGrantForProject)
	tw.temporalWorker.RegisterActivity(zitadelsetup.AddUserGrant)
	tw.temporalWorker.RegisterActivity(zitadelsetup.AddOrganization)
	tw.temporalWorker.RegisterActivity(zitadelsetup.AddProjectGrant)
	tw.temporalWorker.RegisterActivity(zitadelsetup.EnableFeaturesActions)
	tw.temporalWorker.RegisterActivity(zitadelsetup.AddOrUpdateActionTarget)
	tw.temporalWorker.RegisterActivity(zitadelsetup.AddExecutionsOnTarget)
	tw.temporalWorker.RegisterActivity(zitadelsetup.CreateK8sSecrets)
}

func (tw *TemporalWorker) RegisterTemporalSetupWorkflow() {
	temporalSetupWorkflowOptions := workflow.RegisterOptions{
		Name: TemporalSetupWorkflow,
	}

	tw.temporalWorker.RegisterWorkflowWithOptions(temporalsetup.TemporalSetupWorkflow, temporalSetupWorkflowOptions)
	tw.temporalWorker.RegisterActivity(temporalsetup.AddSearchAttributes)
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
func (tw *TemporalWorker) RegisterZitadelSyncWorkflow(entClient *gen.Client, audience, keyFilePath, zitadelProjectID string) {
	// Orchestrator workflow (runs on management worker)
	tw.temporalWorker.RegisterWorkflowWithOptions(
		zitadelsync.ZitadelSyncWorkflow,
		workflow.RegisterOptions{Name: ZitadelSyncWorkflow},
	)

	// Activities object with DI for both workers
	acts := zitadelsync.NewActivities(entClient, tw.cli, audience, keyFilePath, zitadelProjectID)

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
