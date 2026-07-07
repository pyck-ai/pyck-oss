package main

import (
	"context"
	"fmt"
	stdlog "log"
	"net"
	nethttp "net/http"
	"strconv"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/go-chi/chi/v5"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nats.go/micro"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	zitadelsdk "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"

	"github.com/pyck-ai/pyck/backend/bootstrap/pkg/bootstrap"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/handlers"
	"github.com/pyck-ai/pyck/backend/common/hooks"
	"github.com/pyck-ai/pyck/backend/common/http"
	"github.com/pyck-ai/pyck/backend/common/idempotency"
	"github.com/pyck-ai/pyck/backend/common/log"
	logadapter "github.com/pyck-ai/pyck/backend/common/log/adapter"
	"github.com/pyck-ai/pyck/backend/common/nats"
	"github.com/pyck-ai/pyck/backend/common/otel"
	"github.com/pyck-ai/pyck/backend/common/services/temporal"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
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
	"github.com/pyck-ai/pyck/backend/management/workflows"
	zitadelsync "github.com/pyck-ai/pyck/backend/management/workflows/zitadel-sync"
)

const (
	serviceName = "management"
)

func main() {
	// Set up root context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load minimal configuration needed for bootstrap (LogConfig, DbConfig, bootstrap flags).
	// The full configuration cannot be loaded yet because required env vars
	// (PYCK_SERVICE_TOKEN, PYCK_ZITADEL_ORG_ID, etc.) may not exist until
	// after the bootstrap process exports them.
	if err := core.LoadBootstrapEnv(); err != nil {
		stdlog.Fatalf("failed to load bootstrap configuration: %v", err)
		return
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
			bootstrapLogger.Fatal().
				Err(err).
				Msg("Failed during bootstrap")
			return
		}

		// if we're only bootstrapping, we can exit
		if core.BootstrapConfig.BootstrapOnly {
			bootstrapLogger.Info().Msg("Exit after bootstrapping")
			return
		}

		bootstrapLogger.Info().Msg("Continue after bootstrapping")
	}

	// Load full configuration from ENV — bootstrap may have created new secrets
	if err := core.LoadEnv(); err != nil {
		log.ForContext(ctx).Fatal().Err(err).Msg("failed to load configuration")
		return
	}

	// Re-initialize logger with full configuration
	ctx, _ = log.SetupLogger(ctx, serviceName, core.Config.LogConfig)

	// Set up database
	pgxDriver, err := db.NewPostgresMultiDriver(
		serviceName,
		core.Config.DbConfig,
		db.WithWriterIsolation("serializable"),
	)
	if err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("failed setting up database driver")
		return
	}

	// Set up tracer
	tracer, err := otel.SetupTracer(serviceName, core.Config.EnvironmentName, &core.Config.OTelConfig)
	if err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("failed setting up tracer")
		return
	}
	defer tracer.Close()

	// run migrations
	ctx = db.WithMaxRetries(ctx, core.Config.TxRetries)

	if err = db.RunMigrations(
		ctx,
		pgxDriver.DB(),
		serviceName,
		entmigrate.Migrations,
	); err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("failed running migrations")
		return
	}

	// set up ent
	dbClient := ent.NewClient(
		ent.Driver(pgxDriver),
		ent.Log(logadapter.EntLogAdapter(*log.ForContext(ctx))),
	)

	if core.Config.DbDebug {
		dbClient = dbClient.Debug()
	}

	defer func() { _ = dbClient.Close() }()

	dbClient.Use(hooks.LogMutation)

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
		ctx,
		core.Config.ZitadelAudience,
		core.Config.ZitadelGrpcAddr,
		[]string{"openid", "urn:zitadel:iam:org:project:id:zitadel:aud"},
		zitadelOpts...,
	)
	if err != nil {
		log.ForContext(ctx).Fatal().Err(err).Msg("failed to create Zitadel connection")
		return
	}
	defer zitadelConn.Close()

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
	natsClient, err := events.NewNatsClient(ctx, core.Config.NatsUrl)
	if err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("failed setting up NATS client")
		return
	}

	defer natsClient.Close()

	// Set up JetStream
	jetstreamClient, err := events.CreateOrUpdateJetstream(ctx, natsClient, core.Config.NatsStreamName, core.Config.NatsReplicasNumber)
	if err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("failed setting up JetStream")
		return
	}

	// Sub-second eviction of cached entries on tenant-disable NATS events.
	// Pairs with the TenantValidator on the cache-miss path: the next
	// request runs introspect + validate against management, the validator
	// catches the disabled tenant, and the lookup is not re-cached.
	// Restores are no-op here — the missing cache entry just gets rebuilt
	// on the next request once management's /me sees the tenant again.
	revocationCC, err := authn.SubscribeRevocations(
		ctx,
		jetstreamClient,
		core.Config.NatsStreamName,
		serviceName,
		authProvider.OnTenantDisabled,
	)
	if err != nil {
		log.ForContext(ctx).Fatal().Err(err).Msg("failed to subscribe to tenant revocation events")
		return
	}
	defer revocationCC.Stop()

	jetstreamPub, err := events.NewEventPublisher(jetstreamClient, natsClient, core.Config.NatsStreamName, core.Config.NatsReplyTimeout)
	if err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("failed setting up event publisher")
		return
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
	if err := eventSystem.Start(ctx); err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("failed starting event system")
		return
	}
	defer eventSystem.Stop()

	jetstreamSub, err := events.NewEventSubscriber(natsClient, core.Config.NatsStreamName)
	if err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("failed setting up event subscriber")
		return
	}

	defer jetstreamSub.Close()

	// Set up Temporal client
	temporalClient, err := temporal.NewTemporalClient(ctx, core.Config.TemporalUrl)
	if err != nil {
		log.ForContext(ctx).Error().
			Err(err).
			Msg("failed setting up temporal client")
		return
	}

	defer temporalClient.Close()

	// Set up workflow client
	workflowClient, err := workflow.NewClient("", temporalClient)
	if err != nil {
		log.ForContext(ctx).Error().
			Err(err).
			Msg("failed setting up workflow client")
		return
	}

	// Set up event registry
	eventRegistryService, err := service.NewEventRegistryService(dbClient)
	if err != nil {
		log.ForContext(ctx).Error().
			Err(err).
			Msg("failed setting up event registry service")
		return
	}

	err = eventRegistryService.PreloadEventsCache(ctx)
	if err != nil {
		log.ForContext(ctx).Error().
			Err(err).
			Msg("failed preloading events cache")
		return
	}

	cons, err := jetstreamClient.CreateOrUpdateConsumer(context.Background(), core.Config.NatsStreamName, jetstream.ConsumerConfig{
		Name:    fmt.Sprintf("%s-event-registry", serviceName),
		Durable: fmt.Sprintf("%s-event-registry", serviceName),
	})
	if err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("failed setting up JetStream consumer")
		return
	}

	go eventRegistryService.ListenToEvents(ctx, cons)

	// Set up GraphQL resolver
	// authorizer := authz.NewManagementAuthorizer(client)
	dataTypeValidator := validator.NewValidator(service.NewDatabaseDataTypeProvider(dbClient))
	resolver := resolvers.NewResolver(serviceName, dbClient, dataTypeValidator, workflowClient, zitadelConn)

	// Set up temporal worker
	temporalWorker, err := workflows.NewTemporalWorker(temporalClient, workflows.TemporalManagementTaskQueue, resolver.Mutation(), workflows.WorkerOptions{EnableTenantSync: true})
	if err != nil {
		log.ForContext(ctx).Error().
			Err(err).
			Msg("failed setting up temporal worker")
		return
	}

	zitadelSyncInterval, err := time.ParseDuration(core.Config.ZitadelSyncEvery)
	if err != nil || zitadelSyncInterval < 0 {
		log.ForContext(ctx).Error().
			Err(err).
			Msg("invalid ZitadelSyncEvery value")
		return
	}

	nsGetter := workflow.NewNamespaceGetter(core.Config.ZitadelAudience)

	temporalWorker.RegisterTenantWorkflow(dbClient, nsGetter)
	temporalWorker.RegisterDisableTenantWorkflow(zitadelConn)
	temporalWorker.RegisterRestoreTenantWorkflow(zitadelConn)
	temporalWorker.RegisterTenantReconcileWorkflow(dbClient, zitadelConn)
	temporalWorker.RegisterTenantExpiryCheckWorkflow(dbClient)
	temporalWorker.RegisterGenerateJsonSchemaWorkflow()
	temporalWorker.RegisterZitadelSyncWorkflow(dbClient, core.Config.ZitadelOAuthURL, core.Config.ZitadelGrpcAddr, core.Config.ZitadelAudience, core.Config.ZitadelServiceKeyPath, core.Config.ZitadelProjectId, core.Config.ZitadelTlsInsecure)

	err = temporalWorker.Start()
	if err != nil {
		log.ForContext(ctx).Error().
			Err(err).
			Msg("failed starting temporal worker")
		return
	}

	defer temporalWorker.Stop()

	// Subscribe to tenant lifecycle events: the trigger subscriber starts
	// the disable/restore workflows when a tenant's deleted_at transitions.
	triggerCC, err := tenants.SubscribeTrigger(ctx, jetstreamClient, temporalClient, core.Config.NatsStreamName)
	if err != nil {
		log.ForContext(ctx).Error().Err(err).Msg("failed to subscribe tenant lifecycle trigger")
		return
	}
	defer triggerCC.Stop()

	log.ForContext(ctx).Info().Dur("zitadel_sync_interval", zitadelSyncInterval).Msg("orchestrator schedule interval")

	if err := temporalWorker.EnsureZitadelSyncOrchestratorSchedule(ctx, temporalClient, zitadelSyncInterval); err != nil {
		log.ForContext(ctx).Error().Err(err).Msg("failed to ensure orchestrator schedule")
	} else {
		log.ForContext(ctx).Info().Dur("interval", zitadelSyncInterval).Msg("orchestrator schedule ensured")
	}

	tenantReconcileInterval, err := time.ParseDuration(core.Config.TenantReconcileInterval)
	if err != nil || tenantReconcileInterval <= 0 {
		log.ForContext(ctx).Error().Err(err).Msg("invalid TenantReconcileInterval value")
		return
	}

	if err := temporalWorker.EnsureTenantReconcileSchedule(ctx, temporalClient, tenantReconcileInterval); err != nil {
		log.ForContext(ctx).Error().Err(err).Msg("failed to ensure tenant reconcile schedule")
	} else {
		log.ForContext(ctx).Info().Dur("interval", tenantReconcileInterval).Msg("tenant reconcile schedule ensured")
	}

	tenantExpiryCheckInterval, err := time.ParseDuration(core.Config.TenantExpiryCheckInterval)
	if err != nil || tenantExpiryCheckInterval <= 0 {
		log.ForContext(ctx).Error().Err(err).Msg("invalid TenantExpiryCheckInterval value")
		return
	}

	if err := temporalWorker.EnsureTenantExpiryCheckSchedule(ctx, temporalClient, tenantExpiryCheckInterval); err != nil {
		log.ForContext(ctx).Error().Err(err).Msg("failed to ensure tenant expiry check schedule")
	} else {
		log.ForContext(ctx).Info().Dur("interval", tenantExpiryCheckInterval).Msg("tenant expiry check schedule ensured")
	}

	// Set up NATS auth service
	natsAuthService, err := nats.NewAuthService(ctx, serviceName, core.Config.NatsStreamName, authProvider, core.Config.NatsAuthKeySeed)
	if err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("failed setting up NATS auth service")
		return
	}

	_, err = micro.AddService(natsClient, natsAuthService)
	if err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("failed registering NATS auth service")
		return
	}

	// Set up Quickwit forwarder
	//
	// TODO(michael): IMHO, this should be an external client, not part of the
	// management service.
	if core.Config.QuickwitEnabled && core.Config.QuickwitURL != "" {
		quickwitConsumer, err := jetstreamClient.CreateOrUpdateConsumer(ctx, core.Config.NatsStreamName, jetstream.ConsumerConfig{
			Name:          fmt.Sprintf("%s-quickwit-sync", serviceName),
			Durable:       fmt.Sprintf("%s-quickwit-sync", serviceName),
			FilterSubject: fmt.Sprintf("%s.>", core.Config.NatsStreamName),
			AckPolicy:     jetstream.AckExplicitPolicy,
		})
		if err != nil {
			log.ForContext(ctx).Error().
				Err(err).
				Msg("creating quickwit sync consumer")
			return
		}

		quickwitSync := service.NewQuickwitSyncService(
			core.Config.QuickwitURL,
			core.Config.QuickwitBatchSize,
			core.Config.QuickwitBatchTimeout,
		)

		go quickwitSync.ListenToEvents(quickwitConsumer)

		defer quickwitSync.Shutdown()
	}

	// Idempotency store (pyck#1123): writes records inside the mutation
	// transaction via gqltx; janitor goroutine prunes committed rows after
	// the 24h TTL.
	idemStore := newIdempotencyStore(dbClient)
	idempotency.NewJanitor(idemStore, 5*time.Minute, 24*time.Hour).Start(ctx)

	// Set GraphQL server
	gqlServer := handler.NewDefaultServer(resolvers.NewSchema(resolver))

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
		Logger:      log.ForContext(ctx),
	})

	httpRouter.Handle("/", gqlPlaygroundHandler)
	httpRouter.Handle("/query", gqlHandler)
	httpRouter.Handle("/metrics", promhttp.Handler())
	httpRouter.Handle("/health", handlers.NewHealthCheckHandler(db.NewDbHealthChecker(pgxDriver.DB(), entdatatype.Table)))
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

	httpAddr := net.JoinHostPort(core.Config.HTTPHost, strconv.Itoa(core.Config.HTTPPort))
	httpServer := &nethttp.Server{
		Addr:              httpAddr,
		ReadHeaderTimeout: core.Config.HTTPReadHeaderTimeout,
		Handler:           httpRouter,
		BaseContext:       func(_ net.Listener) context.Context { return ctx },
		ErrorLog:          logadapter.StdLogAdapter(*log.ForContext(ctx)),
	}

	log.ForContext(ctx).Info().
		Str("addr", httpServer.Addr).
		Msgf("listening")

	if err := httpServer.ListenAndServe(); err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("http server terminated unexpectedly")
		return
	}
}
