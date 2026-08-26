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
	"github.com/pyck-ai/pyck/backend/common/gqlserver"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/hooks"
	"github.com/pyck-ai/pyck/backend/common/http"
	httpclient "github.com/pyck-ai/pyck/backend/common/http_client"
	"github.com/pyck-ai/pyck/backend/common/idempotency"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/log"
	logadapter "github.com/pyck-ai/pyck/backend/common/log/adapter"
	"github.com/pyck-ai/pyck/backend/common/otel"
	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
	"github.com/pyck-ai/pyck/backend/common/signals"
	"github.com/pyck-ai/pyck/backend/common/startup"
	"github.com/pyck-ai/pyck/backend/common/std"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/validator"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	managementguard "github.com/pyck-ai/pyck/backend/management/guard"
	managementdatatype "github.com/pyck-ai/pyck/backend/management/pkg/datatypes"

	"github.com/pyck-ai/pyck/backend/inventory/core"
	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	_ "github.com/pyck-ai/pyck/backend/inventory/ent/gen/runtime"
	entmigrate "github.com/pyck-ai/pyck/backend/inventory/ent/migrate"
	"github.com/pyck-ai/pyck/backend/inventory/resolvers"
	"github.com/pyck-ai/pyck/backend/inventory/service/stock"
)

const serviceName = "inventory"

func main() {
	os.Exit(realMain())
}

func realMain() int {
	// Root context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Bootstrap logger with default settings; the real level/format come
	// from the environment, but a LoadEnv failure must still be reported
	// structured, so run's error is logged with this context.
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
	// Load configuration
	if err := core.LoadEnv(); err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Configure logger
	procCtx, l := log.SetupLogger(ctx, serviceName, core.Config.LogConfig)
	l.Info().Any("config", core.Config).Msg("starting...")

	// Stop context: cancelled by SIGTERM/SIGINT. Only the startup wait and
	// the HTTP server react to it directly; everything else keeps running
	// on ctx so in-flight requests and the outbox survive the drain.
	appCtx, stopSignals := signals.NotifyContext(procCtx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	// Check the management service is running and version-compatible before
	// starting, because we depend on it for JSON schemas and data types.
	httpClient := httpclient.NewInstrumentedClient(
		core.Config.GatewayHTTPTimeout,
		core.Config.GatewayHTTPMaxIdleConns,
	)
	defer httpClient.CloseIdleConnections()

	mgmtClient := managementapi.NewClient(
		httpClient,
		core.Config.GatewayUrl,
		&clientv2.Options{ParseDataAlongWithErrors: true},
		func(ctx context.Context, r *nethttp.Request, gqlInfo *clientv2.GQLRequestInfo, res any, next clientv2.RequestInterceptorFunc) error {
			r.Header.Set("Authorization", "Bearer "+core.Config.ServiceToken)
			return next(ctx, r, gqlInfo, res)
		},
	)

	if err := managementguard.WaitForManagement(appCtx, mgmtClient, env.GetBuildInfo().GitCommitSHA(), core.Config.StrictVersionCheck); err != nil {
		if abortErr := startup.Check(appCtx, "management dependency check"); abortErr != nil {
			return abortErr
		}
		return fmt.Errorf("management service dependency check failed: %w", err)
	}

	// Set up tracer
	tracer, err := otel.SetupTracer(serviceName, core.Config.EnvironmentName, &core.Config.OTelConfig)
	if err != nil {
		return fmt.Errorf("failed setting up tracer: %w", err)
	}
	defer tracer.Close()

	// Set up database
	pgxDriver, err := db.NewPostgresMultiDriver(
		procCtx,
		serviceName,
		core.Config.DbConfig,
		db.WithWriterIsolation("read committed"),
	)
	if err != nil {
		return fmt.Errorf("failed setting up database driver: %w", err)
	}

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

	dbClient := ent.NewClient(
		ent.Driver(pgxDriver),
		ent.Log(logadapter.EntLogAdapter(*log.ForContext(procCtx))),
	)

	if core.Config.DbDebug {
		dbClient = dbClient.Debug()
	}

	defer func() { _ = dbClient.Close() }()

	dbClient.Use(hooks.LogMutation)

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

	// Auth path: introspection via Zitadel; org-active probe routed
	// through the federation gateway to management's `organization`
	// resolver. The revocation subscriber evicts cached entries within
	// the JetStream-propagation window when a tenant is disabled.
	authProvider, revocationCC, err := authn.NewProviderWithRevocation(
		procCtx,
		zitadel.NewClient(core.Config.ZitadelConfig),
		core.Config.ZitadelConfig,
		managementapi.NewOrganizationValidator(mgmtClient),
		jetstreamClient,
		core.Config.NatsStreamName,
		serviceName,
	)
	if err != nil {
		return fmt.Errorf("failed to set up auth provider: %w", err)
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

	// Set up data types validator
	dataTypesCache, err := json_schema.NewDataTypesCache(procCtx, jetstreamClient, json_schema.DataTypesCacheOptions{
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
		return fmt.Errorf("failed setting up data types cache: %w", err)
	}

	// Drain gate fires while the NATS connection is still open
	listenCtx, stopListening := context.WithCancel(procCtx)
	defer stopListening()
	go dataTypesCache.ListenToEvents(listenCtx)

	if _, err = dataTypesCache.RetrieveJsonSchemasToCache(procCtx); err != nil {
		return fmt.Errorf("failed retrieving initial JSON schemas to cache: %w", err)
	}

	// Set up Inventory Stock Service. The outbox emitter is wired so that
	// createItemMovementViaProc — which INSERTs the movement directly via
	// SQL and bypasses the Ent mutation hook — still produces an outbox row
	// in the same DB transaction. Without this the proc fast path would
	// silently drop the NATS event for every ItemMovement create, and any
	// resolver waiting on a workflow reply would block until OutboxReplyTimeout.
	stockService, err := stock.New(core.Config.DbDriver, eventSystem.EmitEvent)
	if err != nil {
		return fmt.Errorf("failed setting up inventory stock service: %w", err)
	}

	defer stockService.Close()

	if err := startup.Check(appCtx, "stock service"); err != nil {
		return err
	}

	// Set up GraphQL resolver
	// authorizer := authz.NewManagementAuthorizer(client)
	dataTypeValidator := validator.NewValidator(dataTypesCache)
	resolver := resolvers.NewResolver(serviceName, dbClient, dataTypeValidator, stockService)

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

	// Set up GraphQL server
	gqlSchema := resolvers.NewSchema(resolver)
	gqlServer := gqlserver.New(gqlSchema)
	gqlServer.Use(gqltx.NewMiddleware(
		dbClient, ent.NewTxContext, serviceName, core.Config.TxRetries,
		gqltx.WithIdempotency(idemStore, idempotency.DefaultAuthLookup),
		gqltx.WithIdempotencyMaxResponseBytes(core.Config.IdempotencyMaxResponseBytes),
	))
	gqlServer.Use(gqltx.NewWorkflowReplyMiddleware(eventSystem.Registry(), core.Config.OutboxReplyTimeout))
	gqlServer.Use(otel.NewTracingMiddleware())
	gqlServer.Use(resolvers.NewLoaderExtension(dbClient))

	gqlHandler := chi.NewRouter()
	gqlHandler.Use(
		authProvider.HTTPMiddleware(),
		tenant.HTTPMiddleware(),
		gate.HTTPMiddleware(serviceroles.Inventory),
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

	if err := startup.Check(appCtx, "http wiring"); err != nil {
		return err
	}

	if err := httpServer.ListenAndServe(appCtx); err != nil {
		return fmt.Errorf("http server: %w", err)
	}

	l.Info().Msg("shutting down")
	return nil
}
