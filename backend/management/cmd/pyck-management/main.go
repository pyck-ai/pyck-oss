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
	"github.com/go-chi/chi"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nats.go/micro"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/pyck-ai/pyck/backend/bootstrap/pkg/bootstrap"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/handlers"
	"github.com/pyck-ai/pyck/backend/common/hooks"
	"github.com/pyck-ai/pyck/backend/common/http"
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
	pgxDriver, err := db.NewPostgresMultiDriver(serviceName, core.Config.DbConfig)
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

	// Set up auth provider
	zitadelClient := zitadel.NewClient(core.Config.ZitadelConfig)
	authProvider := authn.NewZitadelAuthProvider(zitadelClient, core.Config.ZitadelConfig)

	// run migrations
	ctx = db.WithMaxRetries(ctx, core.Config.TxRetries)

	if err = db.RunMigrations(
		ctx,
		pgxDriver.DB(),
		serviceName,
		core.Config.ServicesPath,
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
	resolver := resolvers.NewResolver(serviceName, dbClient, dataTypeValidator, workflowClient)

	// Set up temporal worker
	temporalWorker, err := workflows.NewTemporalWorker(temporalClient, workflows.TemporalManagementTaskQueue, resolver.Mutation(), workflows.WorkerOptions{EnableTenantSync: true})
	if err != nil {
		log.ForContext(ctx).Error().
			Err(err).
			Msg("failed setting up temporal worker")
		return
	}

	zitadelSyncEvery, err := time.ParseDuration(core.Config.ZitadelSyncEvery)
	if err != nil || zitadelSyncEvery < 0 {
		log.ForContext(ctx).Error().
			Err(err).
			Msg("invalid ZitadelSyncEvery value")
		return
	}

	nsGetter := workflow.NewNamespaceGetter(core.Config.ZitadelAudience)

	temporalWorker.RegisterTenantWorkflow(dbClient, nsGetter)
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

	log.ForContext(ctx).Info().Dur("zitadel_sync_every", zitadelSyncEvery).Msg("orchestrator schedule interval")

	if err := temporalWorker.EnsureZitadelSyncOrchestratorSchedule(ctx, temporalClient, zitadelSyncEvery); err != nil {
		log.ForContext(ctx).Error().Err(err).Msg("failed to ensure orchestrator schedule")
	} else {
		log.ForContext(ctx).Info().Dur("every", zitadelSyncEvery).Msg("orchestrator schedule ensured")
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

	// Set GraphQL server
	gqlServer := handler.NewDefaultServer(resolvers.NewSchema(resolver))

	// Enable introspection only in development
	if core.Config.EnvironmentName == "development" {
		gqlServer.Use(extension.Introspection{})
	}

	gqlServer.Use(gqltx.NewMiddleware(dbClient, ent.NewTxContext, serviceName, core.Config.TxRetries))
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
	httpRouter.Mount("/webhook", webhooks.Router(temporalClient, zitadelsync.TenantSyncTaskQueue))

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
