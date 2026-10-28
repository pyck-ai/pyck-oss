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

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go/micro"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	zitadelsdk "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"

	"github.com/pyck-ai/pyck/backend/bootstrap/pkg/bootstrap"
	"github.com/pyck-ai/pyck/backend/common/admission"
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
	"github.com/pyck-ai/pyck/backend/common/liveness"
	"github.com/pyck-ai/pyck/backend/common/locking"
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
)

const (
	serviceName = "management"

	// registerTenantField is the top-level mutation field the admission cap
	// and the transaction exemption are both configured for: the anonymous
	// tenant registration.
	registerTenantField = "registerTenant"

	// natsAccountCheckTimeout bounds nats.RequireSeparateTenantAccount.
	natsAccountCheckTimeout = 10 * time.Second
)

// eventStreamsLockID serializes event stream changes across management replicas.
var eventStreamsLockID = locking.GenerateLockID(serviceName + "/event-streams") //nolint:gochecknoglobals

// Schedule intervals parse from free-form env strings; these sentinels turn
// the range violations into static errors while the wrapping fmt.Errorf
// names the offending knob.
var (
	errNegativeInterval    = errors.New("interval must not be negative")
	errNonPositiveInterval = errors.New("interval must be positive")
	errPollNotBelowWait    = errors.New("poll interval must be below the wait")

	errNonPositiveConcurrency = errors.New("concurrency must be positive")
)

// validateRegistrationBudgets refuses registration budgets that would break
// registration without an error of their own: a poll of zero or less spins
// Temporal and DB reads for the whole wait, a wait the poll does not fit in
// never re-reads, a Temporal call timeout of zero or less expires every
// trigger delivery before it starts, so the create event is NAKed until
// JetStream drops it and the row stays pending, and a stream lock wait of
// zero or less fails every registerTenant and restoreTenant with
// events.ErrStreamLockNotAcquired.
func validateRegistrationBudgets(wait, poll, temporalCallTimeout, streamLockWait time.Duration) error {
	for _, budget := range []struct {
		name string
		d    time.Duration
	}{
		{"PYCK_TENANT_REGISTRATION_WAIT", wait},
		{"PYCK_TENANT_REGISTRATION_POLL", poll},
		{"PYCK_TENANT_EVENT_TEMPORAL_CALL_TIMEOUT", temporalCallTimeout},
		{"PYCK_EVENT_STREAM_LOCK_WAIT", streamLockWait},
	} {
		if budget.d <= 0 {
			return fmt.Errorf("invalid %s value %s: %w", budget.name, budget.d, errNonPositiveInterval)
		}
	}
	if poll >= wait {
		return fmt.Errorf("invalid PYCK_TENANT_REGISTRATION_POLL value %s with PYCK_TENANT_REGISTRATION_WAIT %s: %w",
			poll, wait, errPollNotBelowWait)
	}
	return nil
}

// graphQLServerConfig is the slice of the service configuration the GraphQL
// extension list is built from, so that list can be assembled (and its
// ordering and per-operation wiring asserted) without the process-wide
// core.Config.
type graphQLServerConfig struct {
	// Introspection exposes the schema; only development sets it.
	Introspection bool
	// RegisterTenantMaxInFlight caps concurrent registerTenant executions
	// on this replica; see the admission package.
	RegisterTenantMaxInFlight int
	// TxRetries is the serialization-failure retry budget of a mutation.
	TxRetries int
	// IdempotencyMaxResponseBytes caps the response cached per key.
	IdempotencyMaxResponseBytes int
}

// graphQLExtensions is the ordered list of gqlgen extensions the management
// GraphQL server runs. The order is behaviour rather than taste: gqlgen
// invokes operation interceptors in registration order, so admission has to
// come before the transaction middleware: an operation refused by the cap
// must be refused before a writer connection is checked out for it.
//
// It is a function taking its configuration rather than reading the
// process-wide one, so the ordering and the registerTenant exemption can be
// asserted without starting the service; realMain only feeds the result to
// Server.Use.
func graphQLExtensions(
	cfg graphQLServerConfig, dbClient *ent.Client, idemStore idempotency.Store,
) []graphql.HandlerExtension {
	exts := make([]graphql.HandlerExtension, 0, 3)

	// Enable introspection only in development
	if cfg.Introspection {
		exts = append(exts, extension.Introspection{})
	}

	// Admission runs BEFORE the transaction middleware: gqlgen invokes
	// operation interceptors in registration order, so a registerTenant
	// refused here never reaches a database connection. registerTenant is
	// anonymous and provisions a whole tenant, which makes the number of
	// them one replica runs at once the only thing standing between a
	// caller and the writer pool.
	exts = append(exts, admission.New(map[string]int{
		registerTenantField: cfg.RegisterTenantMaxInFlight,
	}))

	exts = append(exts, gqltx.NewMiddleware(
		dbClient, ent.NewTxContext, serviceName, cfg.TxRetries,
		gqltx.WithIdempotency(idemStore, idempotency.DefaultAuthLookup),
		gqltx.WithIdempotencyMaxResponseBytes(cfg.IdempotencyMaxResponseBytes),
		// registerTenant commits its tenant row itself and then waits for
		// the registration run that row's event starts; holding the
		// request's writer connection across that wait would pin one
		// connection per in-flight registration.
		gqltx.WithoutTransaction(registerTenantField),
	))

	return exts
}

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
	if err := validateRegistrationBudgets(core.Config.RegistrationWait, core.Config.RegistrationPoll,
		core.Config.TenantEventTemporalCallTimeout, core.Config.EventStreamLockWait); err != nil {
		return err
	}
	if n := core.Config.EventStreamReconcileConcurrency; n <= 0 {
		return fmt.Errorf("invalid PYCK_EVENT_STREAM_RECONCILE_CONCURRENCY value %d: %w", n, errNonPositiveConcurrency)
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

	// Single Zitadel gRPC connection used by the tenant lifecycle workflows
	// (disable/restore) and zitadel-sync. Tenant registration is NOT a
	// consumer: it dials its own per-attempt connection (see
	// register-tenant's dialZitadel), so this connection's sa-admin scope
	// is justified by the lifecycle workflows alone. JWT profile auth from
	// the bootstrap-provisioned sa-admin service account.
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
	// Zitadel client and answers the liveness check that follows in-process
	// from its own tenant table (mgmthandlers.DisabledTenants). No HTTP
	// self-loop; the 6 other services ask the same question through the
	// `disabledTenants` GraphQL query. One source feeds both readers, the
	// auth provider (the caller's own tenant) and the tenant middleware (the
	// acting set), so their verdicts share a cache entry, kept current by
	// the tenant lifecycle subscriber.
	zitadelClient := zitadel.NewClient(core.Config.ZitadelConfig)
	disabledTenantSource := func(ctx context.Context, tenantIDs []uuid.UUID) ([]uuid.UUID, error) {
		return mgmthandlers.DisabledTenants(ctx, dbClient, tenantIDs)
	}
	livenessCache := liveness.NewCacheFromConfig(core.Config.LivenessConfig)
	defer livenessCache.Close()

	// Set up NATS client
	natsClient, err := events.NewNatsClient(procCtx, core.Config.NatsUrl)
	if err != nil {
		return fmt.Errorf("failed setting up NATS client: %w", err)
	}

	defer events.DrainNatsClient(procCtx, natsClient, events.DrainTimeout)

	// Tenant clients must never share the services' account. Checked before
	// anything touches the streams, workflows or event consumers.
	accountCtx, cancelAccount := context.WithTimeout(procCtx, natsAccountCheckTimeout)
	err = nats.RequireSeparateTenantAccount(accountCtx, natsClient, core.Config.NatsTenantAccountName)
	cancelAccount()
	if err != nil {
		return fmt.Errorf("failed checking PYCK_NATS_TENANT_ACCOUNT_NAME: %w", err)
	}

	// Set up JetStream
	streamLimits := events.StreamLimitsOf(core.Config.NatsConfig)

	jetstreamClient, err := events.SetupJetStream(procCtx, natsClient, core.Config.NatsStreamName, core.Config.NatsReplicasNumber, streamLimits)
	if err != nil {
		return fmt.Errorf("failed setting up JetStream: %w", err)
	}

	// Management owns the tenant streams and the aggregate's sources.
	streamLocks := locking.NewPostgresLocks(pgxDriver.DB())
	eventStreams := events.NewStreamTopology(
		jetstreamClient,
		core.Config.NatsStreamName,
		core.Config.NatsReplicasNumber,
		func(ctx context.Context, fn func(context.Context) error) error {
			return streamLocks.WithXactLock(ctx, eventStreamsLockID, fn)
		},
		streamLimits,
		events.WithLockWait(core.Config.EventStreamLockWait),
		events.WithReconcileConcurrency(core.Config.EventStreamReconcileConcurrency),
	)

	// Before the outbox starts, so a tenant's events find its stream.
	if err := tenants.ReconcileStreams(procCtx, dbClient, eventStreams); err != nil {
		return fmt.Errorf("failed setting up the tenant event streams: %w", err)
	}

	if err := startup.Check(appCtx, "nats setup"); err != nil {
		return err
	}

	// The auth provider with its sub-second reaction to tenant lifecycle
	// NATS events: the tenant's liveness verdict is evicted so the next
	// lookup reads the row, and on disable the cached tokens of the
	// tenant's users are dropped. A failed subscribe closes the provider
	// it built.
	authProvider, tenantEventsCC, err := authn.NewProviderWithTenantEvents(
		procCtx,
		zitadelClient,
		core.Config.ZitadelConfig,
		disabledTenantSource,
		livenessCache,
		jetstreamClient,
		core.Config.NatsStreamName,
		serviceName,
	)
	if err != nil {
		return fmt.Errorf("failed to set up auth provider: %w", err)
	}
	// Stops the token cache's janitor goroutine; deferred before the
	// subscriber's Stop so the subscriber stops first.
	defer authProvider.Close()
	defer tenantEventsCC.Stop()

	jetstreamPub := events.NewEventPublisher(jetstreamClient, natsClient, core.Config.NatsStreamName)

	// Set up event system (mutation hook + outbox handler)
	eventSystem := events.NewEventSystem(events.EventSystemConfig[*ent.Tx]{
		ServiceName:       serviceName,
		StreamName:        core.Config.NatsStreamName,
		ConnString:        core.Config.DbMasterUrl,
		Publisher:         jetstreamPub,
		PostCommit:        gqltx.AddPostCommit,
		TxFromContext:     ent.TxFromContext,
		DB:                pgxDriver.DB(),
		Outbox:            core.Config.EventOutboxConfig,
		SelfTenantSchemas: core.SelfTenantSchemas(),
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
	resolver := resolvers.NewResolver(serviceName, dbClient, dataTypeValidator, workflowClient, zitadelConn, eventStreams)

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
	if err := core.Config.WorkerAPIRetry.Validate(); err != nil {
		return err
	}
	if !workerAPIClient.Configured() {
		// Say it at boot: otherwise the first pyck-go registration fails with
		// no obvious cause.
		log.ForContext(ctx).Warn().
			Msg("PYCK_WORKER_API_URL is not set: registering a pyck-go tenant will fail")
	}

	temporalWorker.RegisterTenantWorkflow(dbClient, nsGetter, workerAPIClient)
	if !core.Config.TenantWorkerLifecycle {
		log.ForContext(procCtx).Warn().
			Msg("PYCK_TENANT_WORKER_LIFECYCLE is off: disabling or restoring a pyck-go tenant will not stop or resume its worker")
	}
	temporalWorker.RegisterDisableTenantWorkflow(dbClient, zitadelConn, workerAPIClient, nsGetter, core.Config.TenantWorkerLifecycle)
	temporalWorker.RegisterRestoreTenantWorkflow(dbClient, zitadelConn, workerAPIClient, nsGetter, core.Config.TenantWorkerLifecycle)
	temporalWorker.RegisterTenantReconcileWorkflow(dbClient, zitadelConn, func(ctx context.Context) error {
		return tenants.RepairStreams(ctx, dbClient, eventStreams)
	})
	temporalWorker.RegisterTenantExpiryCheckWorkflow(dbClient)
	temporalWorker.RegisterGenerateJsonSchemaWorkflow()
	temporalWorker.RegisterZitadelSyncWorkflow(dbClient, core.Config.ZitadelOAuthURL, core.Config.ZitadelGrpcAddr, core.Config.ZitadelAudience, core.Config.ZitadelServiceKeyPath, core.Config.ZitadelTlsInsecure)

	err = temporalWorker.Start()
	if err != nil {
		return fmt.Errorf("failed starting temporal worker: %w", err)
	}

	defer temporalWorker.Stop()

	temporalWorker.PromoteVersion(procCtx)

	// Subscribe to tenant lifecycle events: the trigger subscriber starts
	// the disable/restore workflows when a tenant's deleted_at transitions.
	triggerCC, err := tenants.SubscribeTrigger(procCtx, jetstreamClient, temporalClient, core.Config.NatsStreamName,
		tenants.TriggerConfig{
			MaxRedeliver:        core.Config.TenantEventMaxRedeliver,
			TemporalCallTimeout: core.Config.TenantEventTemporalCallTimeout,
		})
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
	natsAuthService, err := nats.NewAuthService(procCtx, serviceName, core.Config.NatsStreamName, core.Config.NatsTenantAccountName, authProvider, core.Config.NatsAuthKeySeed, livenessCache, disabledTenantSource,
		nats.WithCalloutTimeout(core.Config.NatsCalloutTimeout),
		nats.WithUserJWTTTL(core.Config.NatsUserJWTTTL))
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

	gqlExtensionConfig := graphQLServerConfig{
		Introspection:               core.Config.EnvironmentName == "development",
		RegisterTenantMaxInFlight:   core.Config.RegisterTenantMaxInFlight,
		TxRetries:                   core.Config.TxRetries,
		IdempotencyMaxResponseBytes: core.Config.IdempotencyMaxResponseBytes,
	}
	for _, ext := range graphQLExtensions(gqlExtensionConfig, dbClient, idemStore) {
		gqlServer.Use(ext)
	}

	tenantMiddleware, err := tenant.HTTPMiddleware(livenessCache, disabledTenantSource)
	if err != nil {
		return fmt.Errorf("failed setting up tenant middleware: %w", err)
	}

	gqlHandler := chi.NewRouter()
	gqlHandler.Use(
		authProvider.HTTPMiddleware(),
		tenantMiddleware,
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
