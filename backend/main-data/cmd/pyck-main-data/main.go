package main

import (
	"context"
	"net"
	nethttp "net/http"
	"strconv"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/go-chi/chi/v5"
	"github.com/gqlgo/gqlgenc/clientv2"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/env"
	"github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/gate"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/handlers"
	"github.com/pyck-ai/pyck/backend/common/hooks"
	"github.com/pyck-ai/pyck/backend/common/http"
	"github.com/pyck-ai/pyck/backend/common/idempotency"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/log"
	logadapter "github.com/pyck-ai/pyck/backend/common/log/adapter"
	"github.com/pyck-ai/pyck/backend/common/otel"
	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
	"github.com/pyck-ai/pyck/backend/common/std"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/validator"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	managementguard "github.com/pyck-ai/pyck/backend/management/guard"
	managementdatatype "github.com/pyck-ai/pyck/backend/management/pkg/datatypes"

	"github.com/pyck-ai/pyck/backend/main-data/core"
	ent "github.com/pyck-ai/pyck/backend/main-data/ent/gen"
	"github.com/pyck-ai/pyck/backend/main-data/ent/gen/customer"
	_ "github.com/pyck-ai/pyck/backend/main-data/ent/gen/runtime"
	entmigrate "github.com/pyck-ai/pyck/backend/main-data/ent/migrate"
	"github.com/pyck-ai/pyck/backend/main-data/resolvers"
)

const (
	serviceName = "main-data"
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

	// Check the management service is running and version-compatible before
	// starting, because we depend on it for JSON schemas and data types.
	mgmtClient := managementapi.NewClient(
		nethttp.DefaultClient,
		core.Config.GatewayUrl,
		&clientv2.Options{ParseDataAlongWithErrors: true},
		func(ctx context.Context, r *nethttp.Request, gqlInfo *clientv2.GQLRequestInfo, res any, next clientv2.RequestInterceptorFunc) error {
			r.Header.Set("Authorization", "Bearer "+core.Config.ServiceToken)
			return next(ctx, r, gqlInfo, res)
		},
	)

	if err := managementguard.WaitForManagement(ctx, mgmtClient, env.GetBuildInfo().GitCommitSHA(), core.Config.StrictVersionCheck); err != nil {
		log.ForContext(ctx).Fatal().
			Err(err).
			Msg("management service dependency check failed")
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

	// Auth path: introspection via Zitadel; org-active probe routed
	// through the federation gateway to management's `organization`
	// resolver. The revocation subscriber evicts cached entries within
	// the JetStream-propagation window when a tenant is disabled.
	authProvider, revocationCC, err := authn.NewProviderWithRevocation(
		ctx,
		zitadel.NewClient(core.Config.ZitadelConfig),
		core.Config.ZitadelConfig,
		managementapi.NewOrganizationValidator(mgmtClient),
		jetstreamClient,
		core.Config.NatsStreamName,
		serviceName,
	)
	if err != nil {
		log.ForContext(ctx).Fatal().Err(err).Msg("failed to set up auth provider")
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

	// Set up data types validator
	dataTypesCache, err := json_schema.NewDataTypesCache(ctx, jetstreamClient, json_schema.DataTypesCacheOptions{
		Fetcher: managementdatatype.NewDataTypeClient(mgmtClient),
		Stream:  core.Config.NatsStreamName,
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

	// Set up GraphQL server
	dataTypeValidator := validator.NewValidator(dataTypesCache)
	resolver := resolvers.NewResolver(serviceName, dbClient, dataTypeValidator)
	// Idempotency store (pyck#1123): writes records inside the mutation
	// transaction via gqltx; janitor goroutine prunes committed rows after
	// the 24h TTL.
	idemStore := newIdempotencyStore(dbClient)
	idempotency.NewJanitor(idemStore, 5*time.Minute, 24*time.Hour).Start(ctx)

	gqlServer := handler.NewDefaultServer(resolvers.NewSchema(resolver))
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
		gate.HTTPMiddleware(serviceroles.MainData),
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
	httpRouter.Handle("/health", handlers.NewHealthCheckHandler(db.NewDbHealthChecker(pgxDriver.DB(), customer.Table)))

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
