package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	nethttp "net/http"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/go-chi/chi/v5"
	"github.com/nats-io/nats.go/micro"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	zitadelsdk "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"

	"github.com/pyck-ai/pyck/backend/bootstrap/pkg/bootstrap"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/dataindex"
	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/gqlserver"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/hooks"
	"github.com/pyck-ai/pyck/backend/common/http"
	"github.com/pyck-ai/pyck/backend/common/idempotency"
	"github.com/pyck-ai/pyck/backend/common/log"
	logadapter "github.com/pyck-ai/pyck/backend/common/log/adapter"
	"github.com/pyck-ai/pyck/backend/common/nats"
	"github.com/pyck-ai/pyck/backend/common/otel"
	"github.com/pyck-ai/pyck/backend/common/services/temporal"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
	"github.com/pyck-ai/pyck/backend/common/signals"
	"github.com/pyck-ai/pyck/backend/common/startup"
	"github.com/pyck-ai/pyck/backend/common/std"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/validator"
	"github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/management/core"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	entdatatype "github.com/pyck-ai/pyck/backend/management/ent/gen/datatype"
	entmigrate "github.com/pyck-ai/pyck/backend/management/ent/migrate"
	"github.com/pyck-ai/pyck/backend/management/events/tenants"
	"github.com/pyck-ai/pyck/backend/management/github"
	mgmthandlers "github.com/pyck-ai/pyck/backend/management/handlers"
	"github.com/pyck-ai/pyck/backend/management/resolvers"
	"github.com/pyck-ai/pyck/backend/management/service"
	"github.com/pyck-ai/pyck/backend/management/webhooks"
	"github.com/pyck-ai/pyck/backend/management/workerapi"
	"github.com/pyck-ai/pyck/backend/management/workflows"
	zitadelsync "github.com/pyck-ai/pyck/backend/management/workflows/zitadel-sync"
)

const serviceName = "management"

// Schedule intervals parse from free-form env strings; these sentinels turn
// the range violations into static errors while the wrapping fmt.Errorf
// names the offending knob.
var (
	errNegativeInterval    = errors.New("interval must not be negative")
	errNonPositiveInterval = errors.New("interval must be positive")
)

func main() {
	os.Exit(realMain())
}

func realMain() int {
	// Root context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Bootstrap logger with default settings; the real level/format come
	// from the environment, but a LoadBootstrapEnv failure must still be
	// reported structured, so run's error is logged with this context.
	ctx, _ = log.SetupLogger(ctx, serviceName, config.LogConfig{})

	if err := run(ctx); err != nil {
		if errors.Is(err, startup.ErrAborted) {
			log.ForContext(ctx).Info().Err(err).Msg("startup aborted")
			return 0
		}
		log.ForContext(ctx).Error().
			Err(err).
			Msg("service terminated")
		return 1
	}

	return 0
}

// run wires up the service on the given root context and blocks until a stop
// signal has been handled. All teardown happens through its defers, so every
// return path — including startup failures — releases whatever was already
// initialized.
func run(ctx context.Context) error {
	// Load minimal configuration needed for bootstrap (LogConfig, DbConfig, bootstrap flags).
	// The full configuration cannot be loaded yet because required env vars
	// (PYCK_SERVICE_TOKEN, PYCK_ZITADEL_ORG_ID, etc.) may not exist until
	// after the bootstrap process exports them.
	if err := core.LoadBootstrapEnv(); err != nil {
		return fmt.Errorf("failed to load bootstrap configuration: %w", err)
	}

	// Configure logger
	ctx, _ = log.SetupLogger(ctx, serviceName, core.BootstrapConfig.LogConfig)

	log.ForContext(ctx).Info().
		Any("config", core.BootstrapConfig).
		Msg("starting...")

	// Bootstrap
	if core.BootstrapConfig.BootstrapEnabled || isBootstrapMode() {
		bootstrapLogger := log.ForContext(ctx).
			With().
			Str("module", core.BootstrapConfig.BootstrapModule.String()).
			Logger()

		sctx := log.Context(ctx, bootstrapLogger)

		bootstrapLogger.Info().Msg("Running in bootstrap mode")

		// start the bootstrapping process
		if err := bootstrap.Bootstrap(sctx, core.BootstrapConfig.DbConfig, core.BootstrapConfig.BootstrapModule); err != nil {
			return fmt.Errorf("failed during bootstrap: %w", err)
		}

		// if we're only bootstrapping, we can exit
		if core.BootstrapConfig.BootstrapOnly {
			bootstrapLogger.Info().Msg("Exit after bootstrapping")
			return nil
		}

		bootstrapLogger.Info().Msg("Continue after bootstrapping")
	}

	// Load full configuration from ENV — bootstrap may have created new secrets
	if err := core.LoadEnv(); err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Re-initialize logger with full configuration
	procCtx, l := log.SetupLogger(ctx, serviceName, core.Config.LogConfig)

	// Stop context: cancelled by SIGTERM/SIGINT. Only the HTTP server
	// reacts to it directly; everything else keeps running on ctx so
	// in-flight requests and the outbox survive the drain.
	appCtx, stopSignals := signals.NotifyContext(procCtx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	// Set up database
	pgxDriver, err := db.NewPostgresMultiDriver(
		procCtx,
		serviceName,
		core.Config.DbConfig,
		db.WithWriterIsolation("serializable"),
	)
	if err != nil {
		return fmt.Errorf("failed setting up database driver: %w", err)
	}

	// Set up tracer
	tracer, err := otel.SetupTracer(serviceName, core.Config.EnvironmentName, &core.Config.OTelConfig)
	if err != nil {
		return fmt.Errorf("failed setting up tracer: %w", err)
	}
	defer tracer.Close()

	// run migrations
	if err = db.RunMigrations(
		procCtx,
		pgxDriver.DB(),
		serviceName,
		entmigrate.Migrations,
	); err != nil {
		return fmt.Errorf("failed running migrations: %w", err)
	}

	if err := startup.Check(appCtx, "migrations"); err != nil {
		return err
	}

	// set up ent
	dbClient := ent.NewClient(
		ent.Driver(pgxDriver),
		ent.Log(logadapter.EntLogAdapter(*log.ForContext(procCtx))),
	)

	if core.Config.DbDebug {
		dbClient = dbClient.Debug()
	}

	defer func() { _ = dbClient.Close() }()

	dbClient.Use(hooks.LogMutation)

	// The frozen-binding contract: a slot, once bound, keeps its meaning, so a
	// query can trust rows already indexed under it. The lineage reads every
	// version a slug has had, including soft-deleted ones, so recreating a slug
	// cannot silently rebind a slot older rows still fill.
	dbClient.DataType.Use(dataindex.ValidateHook(dataindex.DataTypeFields{
		JSONSchema: entdatatype.FieldJSONSchema,
		Entity:     entdatatype.FieldEntity,
		Slug:       entdatatype.FieldSlug,
	}, dataTypeLineageBindings(dbClient)))

	// Single Zitadel gRPC connection used by the tenant lifecycle
	// workflows (disable/restore/reconcile) AND the organization
	// resolver's v2 SDK calls (GetUserByID + ListOrganizations). JWT
	// profile auth from the bootstrap-provisioned sa-admin service
	// account.
	zitadelOpts := []zitadelsdk.Option{
		zitadelsdk.WithTokenSource(zitadel.NewJWTProfileTokenSource(
			core.Config.ZitadelOAuthURL,
			core.Config.ZitadelAudience,
			core.Config.ZitadelServiceKeyPath,
		)),
	}
	if core.Config.ZitadelTlsInsecure {
		zitadelOpts = append(zitadelOpts, zitadelsdk.WithInsecure())
	}

	zitadelConn, err := zitadelsdk.NewConnection(
		procCtx,
		core.Config.ZitadelAudience,
		core.Config.ZitadelGrpcAddr,
		[]string{"openid", "urn:zitadel:iam:org:project:id:zitadel:aud"},
		zitadelOpts...,
	)
	if err != nil {
		return fmt.Errorf("failed to create Zitadel connection: %w", err)
	}
	defer zitadelConn.Close()

	if err := startup.Check(appCtx, "zitadel connection"); err != nil {
		return err
	}

	// Set up auth provider. Management introspects via the standard
	// Zitadel client and runs the org-active check in-process: the
	// validator is an inline closure that calls the local v2 SDK
	// helper (mgmthandlers.ResolveOrganization) against the same system
	// zitadelConn the workflows use. No HTTP self-loop, same Zitadel
	// surface the 6 other services see through the `organization`
	// GraphQL query.
	zitadelClient := zitadel.NewClient(core.Config.ZitadelConfig)
	orgValidator := func(ctx context.Context, sub string) (bool, error) {
		result, err := mgmthandlers.ResolveOrganization(ctx, zitadelConn, sub)
		if err != nil {
			return false, err
		}
		return result.Active, nil
	}
	authProvider := authn.NewZitadelAuthProvider(zitadelClient, core.Config.ZitadelConfig, orgValidator)

	// Set up NATS client
	natsClient, err := events.NewNatsClient(procCtx, core.Config.NatsUrl)
	if err != nil {
		return fmt.Errorf("failed setting up NATS client: %w", err)
	}

	defer events.DrainNatsClient(procCtx, natsClient, events.DrainTimeout)

	// Set up JetStream
	jetstreamClient, err := events.CreateOrUpdateJetstream(procCtx, natsClient, core.Config.NatsStreamName, core.Config.NatsReplicasNumber)
	if err != nil {
		return fmt.Errorf("failed setting up JetStream: %w", err)
	}

	if err := startup.Check(appCtx, "nats setup"); err != nil {
		return err
	}

	// Sub-second eviction of cached entries on tenant-disable NATS events.
	// Pairs with the TenantValidator on the cache-miss path: the next
	// request runs introspect + validate against management, the validator
	// catches the disabled tenant, and the lookup is not re-cached.
	// Restores are no-op here — the missing cache entry just gets rebuilt
	// on the next request once management's /me sees the tenant again.
	revocationCC, err := authn.SubscribeRevocations(
		procCtx,
		jetstreamClient,
		core.Config.NatsStreamName,
		serviceName,
		authProvider.OnTenantDisabled,
	)
	if err != nil {
		return fmt.Errorf("failed to subscribe to tenant revocation events: %w", err)
	}
	defer revocationCC.Stop()

	jetstreamPub, err := events.NewEventPublisher(jetstreamClient, natsClient, core.Config.NatsStreamName, core.Config.NatsReplyTimeout)
	if err != nil {
		return fmt.Errorf("failed setting up event publisher: %w", err)
	}

	// Set up event system (mutation hook + outbox handler)
	eventSystem := events.NewEventSystem(events.EventSystemConfig[*ent.Tx]{
		ServiceName:   serviceName,
		StreamName:    core.Config.NatsStreamName,
		ConnString:    core.Config.DbMasterUrl,
		Publisher:     jetstreamPub,
		PostCommit:    gqltx.AddPostCommit,
		TxFromContext: ent.TxFromContext,
		DB:            pgxDriver.DB(),
		Outbox:        core.Config.EventOutboxConfig,
	})

	dbClient.Use(eventSystem.Hook())
	if err := eventSystem.Start(procCtx); err != nil {
		return fmt.Errorf("failed starting event system: %w", err)
	}
	defer eventSystem.Stop()

	// Set up Temporal client
	temporalClient, err := temporal.NewTemporalClient(procCtx, core.Config.TemporalUrl, core.Config.TemporalDialTimeout)
	if err != nil {
		return fmt.Errorf("failed setting up temporal client: %w", err)
	}

	defer temporalClient.Close()

	if err := startup.Check(appCtx, "temporal client"); err != nil {
		return err
	}

	// Set up workflow client
	workflowClient, err := workflow.NewClient("", temporalClient)
	if err != nil {
		return fmt.Errorf("failed setting up workflow client: %w", err)
	}

	// Set up GraphQL resolver
	// authorizer := authz.NewManagementAuthorizer(client)
	dataTypeValidator := validator.NewValidator(service.NewDatabaseDataTypeProvider(dbClient))
	resolver := resolvers.NewResolver(serviceName, dbClient, dataTypeValidator, workflowClient, zitadelConn)

	// Set up temporal worker
	temporalWorker, err := workflows.NewTemporalWorker(
		temporalClient,
		workflows.TemporalManagementTaskQueue,
		resolver.Mutation(),
		workflows.WorkerOptions{EnableTenantSync: true, Versioning: core.Config.VersioningConfig},
	)
	if err != nil {
		return fmt.Errorf("failed setting up temporal worker: %w", err)
	}

	log.ForContext(procCtx).Info().
		Bool("versioned", temporalWorker.Versioned()).
		Msg("temporal worker deployment versioning")

	zitadelSyncInterval, err := time.ParseDuration(core.Config.ZitadelSyncEvery)
	if err != nil {
		return fmt.Errorf("invalid ZitadelSyncEvery value: %w", err)
	}
	if zitadelSyncInterval < 0 {
		return fmt.Errorf("invalid ZitadelSyncEvery value %s: %w", zitadelSyncInterval, errNegativeInterval)
	}

	nsGetter := workflow.NewNamespaceGetter(core.Config.ZitadelAudience)
	workerAPIClient := workerapi.New(core.Config.WorkerAPIURL, core.Config.ServiceToken)
	if !workerAPIClient.Configured() {
		// Say it at boot: otherwise the first pyck-go registration fails with
		// no obvious cause.
		log.ForContext(ctx).Warn().
			Msg("PYCK_WORKER_API_URL is not set: registering a pyck-go tenant will fail")
	}

	temporalWorker.RegisterTenantWorkflow(dbClient, nsGetter, workerAPIClient)
	temporalWorker.RegisterDisableTenantWorkflow(zitadelConn)
	temporalWorker.RegisterRestoreTenantWorkflow(zitadelConn)
	temporalWorker.RegisterTenantReconcileWorkflow(dbClient, zitadelConn)
	temporalWorker.RegisterTenantExpiryCheckWorkflow(dbClient)
	temporalWorker.RegisterGenerateJsonSchemaWorkflow()
	temporalWorker.RegisterZitadelSyncWorkflow(dbClient, core.Config.ZitadelOAuthURL, core.Config.ZitadelGrpcAddr, core.Config.ZitadelAudience, core.Config.ZitadelServiceKeyPath, core.Config.ZitadelProjectId, core.Config.ZitadelTlsInsecure)

	err = temporalWorker.Start()
	if err != nil {
		return fmt.Errorf("failed starting temporal worker: %w", err)
	}

	defer temporalWorker.Stop()

	temporalWorker.PromoteVersion(procCtx)

	// Subscribe to tenant lifecycle events: the trigger subscriber starts
	// the disable/restore workflows when a tenant's deleted_at transitions.
	triggerCC, err := tenants.SubscribeTrigger(procCtx, jetstreamClient, temporalClient, core.Config.NatsStreamName)
	if err != nil {
		return fmt.Errorf("failed to subscribe tenant lifecycle trigger: %w", err)
	}
	defer triggerCC.Stop()

	log.ForContext(procCtx).Info().Dur("zitadel_sync_interval", zitadelSyncInterval).Msg("orchestrator schedule interval")

	if err := temporalWorker.EnsureZitadelSyncOrchestratorSchedule(procCtx, temporalClient, zitadelSyncInterval); err != nil {
		log.ForContext(procCtx).Error().Err(err).Msg("failed to ensure orchestrator schedule")
	} else {
		log.ForContext(procCtx).Info().Dur("interval", zitadelSyncInterval).Msg("orchestrator schedule ensured")
	}

	tenantReconcileInterval, err := time.ParseDuration(core.Config.TenantReconcileInterval)
	if err != nil {
		return fmt.Errorf("invalid TenantReconcileInterval value: %w", err)
	}
	if tenantReconcileInterval <= 0 {
		return fmt.Errorf("invalid TenantReconcileInterval value %s: %w", tenantReconcileInterval, errNonPositiveInterval)
	}

	if err := temporalWorker.EnsureTenantReconcileSchedule(procCtx, temporalClient, tenantReconcileInterval); err != nil {
		log.ForContext(procCtx).Error().Err(err).Msg("failed to ensure tenant reconcile schedule")
	} else {
		log.ForContext(procCtx).Info().Dur("interval", tenantReconcileInterval).Msg("tenant reconcile schedule ensured")
	}

	tenantExpiryCheckInterval, err := time.ParseDuration(core.Config.TenantExpiryCheckInterval)
	if err != nil {
		return fmt.Errorf("invalid TenantExpiryCheckInterval value: %w", err)
	}
	if tenantExpiryCheckInterval <= 0 {
		return fmt.Errorf("invalid TenantExpiryCheckInterval value %s: %w", tenantExpiryCheckInterval, errNonPositiveInterval)
	}

	if err := temporalWorker.EnsureTenantExpiryCheckSchedule(procCtx, temporalClient, tenantExpiryCheckInterval); err != nil {
		log.ForContext(procCtx).Error().Err(err).Msg("failed to ensure tenant expiry check schedule")
	} else {
		log.ForContext(procCtx).Info().Dur("interval", tenantExpiryCheckInterval).Msg("tenant expiry check schedule ensured")
	}

	if err := startup.Check(appCtx, "schedules"); err != nil {
		return err
	}

	// Set up NATS auth service
	natsAuthService, err := nats.NewAuthService(procCtx, serviceName, core.Config.NatsStreamName, authProvider, core.Config.NatsAuthKeySeed)
	if err != nil {
		return fmt.Errorf("failed setting up NATS auth service: %w", err)
	}

	_, err = micro.AddService(natsClient, natsAuthService)
	if err != nil {
		return fmt.Errorf("failed registering NATS auth service: %w", err)
	}

	// Idempotency store (pyck#1123): writes records inside the mutation
	// transaction via gqltx; janitor goroutine prunes committed rows after
	// the 24h TTL.
	idemStore := newIdempotencyStore(dbClient)

	// Scoped janitor context: its cancel is registered after the
	// dbClient.Close defer, so the pruning loop is told to stop before the
	// DB client it queries goes away.
	janitorCtx, stopJanitor := context.WithCancel(procCtx)
	defer stopJanitor()
	idempotency.NewJanitor(idemStore, 5*time.Minute, 24*time.Hour).Start(janitorCtx)

	// Set GraphQL server
	gqlServer := gqlserver.New(resolvers.NewSchema(resolver))

	// Enable introspection only in development
	if core.Config.EnvironmentName == "development" {
		gqlServer.Use(extension.Introspection{})
	}

	gqlServer.Use(gqltx.NewMiddleware(
		dbClient, ent.NewTxContext, serviceName, core.Config.TxRetries,
		gqltx.WithIdempotency(idemStore, idempotency.DefaultAuthLookup),
		gqltx.WithIdempotencyMaxResponseBytes(core.Config.IdempotencyMaxResponseBytes),
	))
	gqlServer.Use(gqltx.NewWorkflowReplyMiddleware(eventSystem.Registry(), core.Config.OutboxReplyTimeout))

	gqlHandler := chi.NewRouter()
	gqlHandler.Use(
		authProvider.HTTPMiddleware(),
		tenant.HTTPMiddleware(),
		feature.HTTPMiddleware(),
	)
	gqlHandler.Mount("/", gqlServer)

	gqlPlayground := playground.Handler(std.Title(serviceName), "/query")
	gqlPlaygroundHandler := chi.NewRouter()
	gqlPlaygroundHandler.Mount("/", gqlPlayground)

	// Set up HTTP server
	httpRouter := http.NewRouter(http.RouterConfig{
		ServiceName: serviceName,
		Logger:      log.ForContext(procCtx),
	})

	// Requests get their own cancellable context below procCtx. It stays
	// open through the whole drain; being the last-registered defer it is
	// the FIRST to fire on unwind, so requests that outlived the drain
	// deadline are cancelled and abort through their own error paths before
	// the pools and clients they use start closing.
	reqCtx, cancelRequests := context.WithCancel(procCtx)
	defer cancelRequests()

	httpAddr := net.JoinHostPort(core.Config.HTTPHost, strconv.Itoa(core.Config.HTTPPort))
	httpServer := http.NewServer(
		&nethttp.Server{
			Addr:              httpAddr,
			ReadHeaderTimeout: core.Config.HTTPReadHeaderTimeout,
			Handler:           httpRouter,
			BaseContext:       func(_ net.Listener) context.Context { return reqCtx },
			ErrorLog:          logadapter.StdLogAdapter(*log.ForContext(procCtx)),
		},
		http.ServerOptions{
			DrainTimeout:  core.Config.HTTPShutdownDrainTimeout,
			PreDrainDelay: core.Config.HTTPShutdownPreDrainDelay,
		},
	)

	httpRouter.Handle("/", gqlPlaygroundHandler)
	httpRouter.Handle("/query", gqlHandler)
	httpRouter.Handle("/metrics", promhttp.Handler())
	// Liveness: static 200, no dependency checks — see http.LivenessHandler.
	httpRouter.Handle("/health", http.LivenessHandler())
	// Readiness: 503 the moment shutdown begins, so routing stops before
	// the listener closes.
	httpRouter.Handle("/health/ready", httpServer.ReadinessHandler(db.NewDbHealthChecker(pgxDriver.HealthDB())))
	httpRouter.Handle("/static/settings.json", mgmthandlers.NewSettingsHandler(core.Config.FrontendConfig))
	httpRouter.Mount("/github", github.Router(core.Config.GithubClientID, core.Config.GithubClientSecret))
	httpRouter.Mount("/webhook", webhooks.Router(webhooks.Config{
		TemporalClient:       temporalClient,
		TenantSyncTaskQueue:  zitadelsync.TenantSyncTaskQueue,
		ZitadelAudience:      core.Config.ZitadelAudience,
		ZitadelActionSignKey: core.Config.ZitadelActionSigningKey,

		Publisher:                 jetstreamPub,
		NatsStreamName:            core.Config.NatsStreamName,
		ZitadelLoginActionSignKey: core.Config.ZitadelLoginActionSigningKey,
	}))

	if err := startup.Check(appCtx, "http wiring"); err != nil {
		return err
	}

	if err := httpServer.ListenAndServe(appCtx); err != nil {
		return fmt.Errorf("http server: %w", err)
	}

	l.Info().Msg("shutting down")
	return nil
}
