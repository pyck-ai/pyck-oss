package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	_ "time/tzdata" // embed tzdata as a fallback

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/log"
	logadapter "github.com/pyck-ai/pyck/backend/common/log/adapter"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
	"github.com/pyck-ai/pyck/backend/temporal/authz"
	"github.com/pyck-ai/pyck/backend/temporal/config"
	temporalevent "github.com/pyck-ai/pyck/backend/temporal/event"
	"github.com/pyck-ai/pyck/backend/temporal/event/adapter"
	"github.com/urfave/cli/v2"
	"go.temporal.io/server/common/authorization"
	"go.temporal.io/server/common/build"
	temporalconfig "go.temporal.io/server/common/config"
	"go.temporal.io/server/common/debug"
	"go.temporal.io/server/common/dynamicconfig"
	"go.temporal.io/server/common/headers"
	temporallog "go.temporal.io/server/common/log"
	"go.temporal.io/server/temporal"
)

var (
	ErrUnknownInterceptor = errors.New("unknown event interceptor type")
	ErrAlreadyStarted     = errors.New("temporal server already started")
)

func runServer(c *cli.Context) error {
	ctx := context.WithoutCancel(c.Context)

	// Load configuration
	if err := config.LoadEnv(ctx); err != nil {
		return fmt.Errorf("failed loading config from environment: %w", err)
	}

	// Configure logger
	ctx, logger := log.SetupLogger(ctx, serviceName, config.Config.LogConfig)

	log.ForContext(ctx).Info().
		Any("config", config.Config).
		Msg("starting...")

	// Set up NATS client
	natsClient, err := events.NewNatsClient(ctx, config.Config.NatsUrl)
	if err != nil {
		return fmt.Errorf("failed setting up nats client: %w", err)
	}

	defer natsClient.Close()

	// Set up JetStream
	jetstreamClient, err := events.CreateOrUpdateJetstream(ctx, natsClient, config.Config.NatsStreamName, config.Config.NatsReplicasNumber)
	if err != nil {
		return fmt.Errorf("failed setting up jetstream: %w", err)
	}

	jetstreamPub, err := events.NewEventPublisher(jetstreamClient, natsClient, config.Config.NatsStreamName, config.Config.NatsReplyTimeout)
	if err != nil {
		return fmt.Errorf("failed setting up event publisher: %w", err)
	}

	jetstreamSub, err := events.NewEventSubscriber(natsClient, config.Config.NatsStreamName)
	if err != nil {
		return fmt.Errorf("failed setting up event subscriber: %w", err)
	}

	defer jetstreamSub.Close()

	// Set up event handler for Temporal workflow events
	eventHandler := temporalevent.NewHandler(ctx, jetstreamPub, config.Config.EventWorkerConfig)
	defer eventHandler.Close()

	// Set up Temporal Server
	serverCtx := context.WithoutCancel(ctx)
	serverCtx, shutdown := signal.NotifyContext(serverCtx, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer shutdown()

	server := NewTemporalServer(eventHandler, logger)

	if err := server.Start(ctx); err != nil {
		return fmt.Errorf("failed starting temporal server: %w", err)
	}

	<-serverCtx.Done()

	if err := server.Stop(); err != nil {
		return fmt.Errorf("failed stopping temporal server: %w", err)
	}

	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("terminating due to error: %w", ctx.Err())
	}

	log.ForContext(ctx).Info().
		Msg("shutting down...")

	return nil
}

type temporalServer struct {
	mu           sync.Mutex
	eventHandler *temporalevent.Handler
	logger       log.Logger
	server       temporal.Server
	pgAdapter    *adapter.PostgresAdapter
}

func NewTemporalServer(eventHandler *temporalevent.Handler, logger log.Logger) *temporalServer {
	return &temporalServer{
		eventHandler: eventHandler,
		logger:       logger,
	}
}

func (s *temporalServer) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.server != nil {
		return ErrAlreadyStarted
	}

	opts := []temporal.ServerOption{}

	cfg, err := temporalconfig.LoadConfig("docker", "./config", "")
	if err != nil {
		return err
	}

	s.logger.Debug().
		Any("config", cfg).
		Any("build-info", build.InfoData).
		Any("server-version", headers.ServerVersion).
		Any("debug-enabled", debug.Enabled).
		Msg("loaded temporal configuration")

	// Set up Temporal logger
	temporalLogLevel, err := log.ParseLevel(cfg.Log.Level)
	if err != nil {
		s.logger.Warn().
			Err(err).
			Str("level", cfg.Log.Level).
			Msg("failed to parse log level from config, defaulting to info")
		temporalLogLevel = log.InfoLevel
	}

	temporalLogger := s.logger.Level(temporalLogLevel)

	serverLogger := logadapter.TemporalLogAdapter(
		temporalLogger.With().
			Str("component", "temporal-server").
			Logger(),
	)
	dynamicconfigLogger := logadapter.TemporalLogAdapter(
		temporalLogger.With().
			Str("component", "temporal-dynamicconfig").
			Logger(),
	)
	namespaceLogger := logadapter.TemporalLogAdapter(
		temporalLogger.With().
			Str("component", "temporal-namespace").
			Logger(),
	)

	// We override Temporal's logger configuration, because we use our own
	// logger with it's own configuration...
	cfg.Log = temporallog.Config{
		Development: false,
		Stdout:      false,
		Level:       temporalLogLevel.String(),
	}

	// Set up dynamic config client
	var dynamicConfigClient dynamicconfig.Client

	if cfg.DynamicConfigClient != nil {
		dynamicConfigClient, err = dynamicconfig.NewFileBasedClient(cfg.DynamicConfigClient, dynamicconfigLogger, nil)
		if err != nil {
			return fmt.Errorf("failed to create dynamic config client: %w", err)
		}
	} else {
		dynamicConfigClient = dynamicconfig.NewNoopClient()
		s.logger.Info().
			Msg("Dynamic config client is not configured. Using noop client.")
	}

	// The official Docker image and the official Helm Chart have different
	// configuration templates. The Helm Chart does not allow defining a custom
	// claim mapper or authorizer. Therefore, we always override these settings
	// to ensure compatibility.
	if cfg.Global.Authorization.ClaimMapper != "pyck" {
		s.logger.Warn().
			Str("claim-mapper", cfg.Global.Authorization.ClaimMapper).
			Msg("overriding configured claim mapper, only 'pyck' is supported")
		cfg.Global.Authorization.ClaimMapper = "pyck"
	}

	if cfg.Global.Authorization.Authorizer != "default" {
		s.logger.Warn().
			Str("authorizer", cfg.Global.Authorization.Authorizer).
			Msg("overriding configured authorizer, only 'default' is supported")
		cfg.Global.Authorization.Authorizer = "default"
	}

	// Set up claim mapper, authorizer and audience mapper
	zitadelClient := zitadel.NewClient(config.Config.ZitadelConfig)
	authProvider := authn.NewZitadelAuthProvider(zitadelClient, config.Config.ZitadelConfig)
	claimMapper := authz.NewClaimMapper(ctx, authProvider)

	audienceMapper, err := authorization.GetAudienceMapperFromConfig(&cfg.Global.Authorization)
	if err != nil {
		return fmt.Errorf("unable to instantiate audience mapper: %w", err)
	}

	authorizer, err := authorization.GetAuthorizerFromConfig(&cfg.Global.Authorization)
	if err != nil {
		return fmt.Errorf("failed to create authorizer: %w", err)
	}

	if authorization.IsNoopAuthorizer(authorizer) {
		s.logger.Warn().
			Msg("no base authorizer configured, all requests will be allowed by default!")
	} else {
		s.logger.Info().
			Str("authorizer", cfg.Global.Authorization.Authorizer).
			Msg("using configured base authorizer")
	}

	authorizer = authz.NewAuthorizer(ctx, authorizer)

	// Set up namespace filter
	nsFilter := authz.NewNamespaceFilter(ctx)
	opts = append(opts, temporal.WithChainedFrontendGrpcInterceptors(nsFilter))

	// Set up event adapter
	switch config.Config.EventAdapter {
	case config.AdapterTypeDefault, config.AdapterTypePostgresListen:
		// PostgreSQL LISTEN/NOTIFY adapter - real-time events using database triggers
		// This reuses Temporal's existing SQL configuration for the visibility store
		postgresAdapter, err := adapter.NewPostgresAdapter(s.eventHandler, config.Config.EventAdapterPostgresListenChannel, cfg)
		if err != nil {
			return fmt.Errorf("failed to create PostgreSQL LISTEN adapter: %w", err)
		}

		s.pgAdapter = postgresAdapter

		s.logger.Info().
			Str("channel", config.Config.EventAdapterPostgresListenChannel).
			Msg("using PostgreSQL LISTEN/NOTIFY event adapter")
	case config.AdapterTypeGRPC:
		grpcInterceptor := adapter.NewGRPCInterceptor(ctx, s.eventHandler)
		opts = append(opts, temporal.WithChainedFrontendGrpcInterceptors(grpcInterceptor))

		s.logger.Info().
			Msg("using gRPC event adapter")
	default:
		// This should never happen due to envconfig validation...
		return fmt.Errorf("%w: %q", ErrUnknownInterceptor, config.Config.EventAdapter)
	}

	// Set up Temporal server
	services := make([]string, 0, len(cfg.Services))

	for name := range cfg.Services {
		services = append(services, name)
	}

	opts = append(opts,
		temporal.ForServices(services),
		temporal.WithConfig(cfg),
		temporal.WithDynamicConfigClient(dynamicConfigClient),
		temporal.WithLogger(serverLogger),
		temporal.WithNamespaceLogger(namespaceLogger),
		temporal.WithAuthorizer(authorizer),
		temporal.WithClaimMapper(func(*temporalconfig.Config) authorization.ClaimMapper {
			return claimMapper
		}),
		temporal.WithAudienceGetter(func(cfg *temporalconfig.Config) authorization.JWTAudienceMapper {
			return audienceMapper
		}),
	)

	server, err := temporal.NewServer(opts...)
	if err != nil {
		return err
	}

	s.server = server

	// Start the Temporal server first so it can create the visibility DB.
	if err := s.server.Start(); err != nil {
		return err
	}

	// Start the PostgreSQL adapter afterwards in a separate goroutine so the
	// server startup is not blocked by adapter retries.
	if s.pgAdapter != nil {
		if err := s.pgAdapter.Start(ctx); err != nil {
			s.logger.Error().
				Err(err).
				Msg("failed to start PostgreSQL LISTEN adapter after temporal server start")
		}
	}

	return nil
}

func (s *temporalServer) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pgAdapter != nil {
		s.logger.Info().
			Msg("stopping PostgreSQL LISTEN adapter...")

		if err := s.pgAdapter.Stop(); err != nil {
			return fmt.Errorf("failed to stop PostgreSQL LISTEN adapter: %w", err)
		}

		s.pgAdapter = nil
	}

	if s.server != nil {
		s.logger.Info().
			Msg("stopping Temporal server...")

		if err := s.server.Stop(); err != nil {
			return fmt.Errorf("failed to stop Temporal server: %w", err)
		}

		s.server = nil
	}

	return nil
}
