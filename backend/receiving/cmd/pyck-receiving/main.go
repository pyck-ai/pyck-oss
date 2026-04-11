package main

import (
	"context"
	"net"
	nethttp "net/http"
	"strconv"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/go-chi/chi"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/handlers"
	"github.com/pyck-ai/pyck/backend/common/hooks"
	"github.com/pyck-ai/pyck/backend/common/http"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/log"
	logadapter "github.com/pyck-ai/pyck/backend/common/log/adapter"
	"github.com/pyck-ai/pyck/backend/common/otel"
	"github.com/pyck-ai/pyck/backend/common/services/temporal"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
	"github.com/pyck-ai/pyck/backend/common/std"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/validator"

	"github.com/pyck-ai/pyck/backend/receiving/core"
	ent "github.com/pyck-ai/pyck/backend/receiving/ent/gen"
	entinbound "github.com/pyck-ai/pyck/backend/receiving/ent/gen/inbound"
	_ "github.com/pyck-ai/pyck/backend/receiving/ent/gen/runtime"
	"github.com/pyck-ai/pyck/backend/receiving/resolvers"
)

const (
	serviceName = "receiving"
)

func main() {
	// Set up root context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up default logger
	ctx, _ = log.SetupLogger(ctx, serviceName, config.LogConfig{})

	// Load configuration
	if err := core.LoadEnv(); err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("failed to load configuration")
		return
	}

	// Configure logger
	ctx, _ = log.SetupLogger(ctx, serviceName, core.Config.LogConfig)

	log.ForContext(ctx).Info().
		Any("config", core.Config).
		Msg("starting...")

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

	// Set up database
	pgxDriver, err := db.NewPostgresMultiDriver(serviceName, core.Config.DbConfig)
	if err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("failed setting up database driver")
		return
	}

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

	dbClient := ent.NewClient(
		ent.Driver(pgxDriver),
		ent.Log(logadapter.EntLogAdapter(*log.ForContext(ctx))),
	)

	if core.Config.DbDebug {
		dbClient = dbClient.Debug()
	}

	defer func() { _ = dbClient.Close() }()

	dbClient.Use(hooks.LogMutation)

	// Set up NATS client (needed before MutationEventHook for FieldChangeEmitter)
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

	// Set up data types validator
	dataTypesCache, err := json_schema.NewDataTypesCache(ctx, jetstreamClient, json_schema.DataTypesCacheOptions{
		GatewayURL: core.Config.GatewayUrl,
		JwtToken:   core.Config.ServiceToken,
		Stream:     core.Config.NatsStreamName,
		Topics: []string{
			core.Config.NatsStreamName + ".*.crud.management.datatype.*.create",
			core.Config.NatsStreamName + ".*.crud.management.datatype.*.update",
			core.Config.NatsStreamName + ".*.crud.management.datatype.*.delete",
		},
		ServiceName: serviceName + "_" + core.Config.ServiceInstanceID,
	})
	if err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("failed setting up data types cache")
		return
	}

	go dataTypesCache.ListenToEvents(ctx)

	if err = dataTypesCache.RetrieveJsonSchemasToCache(ctx); err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("failed retrieving initial JSON schemas to cache")
		return
	}

	// Set up Temporal
	temporalClient, err := temporal.NewTemporalClient(ctx, core.Config.TemporalUrl)
	if err != nil {
		log.ForContext(ctx).Error().
			Err(err).
			Msg("failed setting up temporal client")
		return
	}

	defer temporalClient.Close()

	// Set up GraphQL server
	dataTypeValidator := validator.NewValidator(dataTypesCache)
	resolver := resolvers.NewResolver(serviceName, dbClient, dataTypeValidator)
	gqlServer := handler.NewDefaultServer(resolvers.NewSchema(resolver))
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
	httpRouter.Handle("/health", handlers.NewHealthCheckHandler(db.NewDbHealthChecker(pgxDriver.DB(), entinbound.Table)))

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
