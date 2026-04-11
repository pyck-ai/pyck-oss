package main

import (
	"context"
	"flag"

	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/services/temporal"

	"github.com/pyck-ai/pyck/backend/management/core"
	"github.com/pyck-ai/pyck/backend/management/workflows"
)

func isBootstrapMode() bool {
	runBootstrapOnly := flag.Bool("bootstrap-only", false, "Run only the worker processes to set up the environment for other services")
	flag.Parse()
	return *runBootstrapOnly
}

// TODO(michael): This should be moved to a sub-command. Also, there is no need
// to have this run as a long-running process. The deployment could directly
// execute this sub-command instead of spawning a worker and waiting for
// pyck-cli to trigger the workflow.
func runBootstrap(ctx context.Context) {
	logger := log.ForContext(ctx)

	logger.Info().
		Msgf("Running %q app in bootstrap mode", serviceName)

	if err := core.LoadBootstrapEnv(); err != nil {
		logger.Fatal().
			Err(err).
			Msg("failed to load configuration")
		return
	}

	log.ForContext(ctx).Info().
		Any("config", core.BootstrapConfig).
		Msg("bootstrapping...")

	temporalURL := core.BootstrapConfig.TemporalBootstrapUrl
	if temporalURL == "" {
		temporalURL = core.BootstrapConfig.TemporalUrl
	}

	namespaceClient, err := temporal.NewTemporalNamespaceClient(ctx, temporalURL)
	if err != nil {
		logger.Error().
			Err(err).
			Msg("temporal namespace client was not initialized")
		return
	}

	defer namespaceClient.Close()

	err = temporal.CreateTemporalNamespace(ctx, namespaceClient, defaultTemporalNamespace)
	if err != nil {
		logger.Error().
			Err(err).
			Msg("failed creating default temporal namespace. Maybe it already exists. Moving on..")
	}

	temporalClient, err := temporal.NewTemporalClient(ctx, core.BootstrapConfig.TemporalBootstrapUrl)
	if err != nil {
		logger.Error().
			Err(err).
			Msg("temporal client was not initialized")
		return
	}

	defer temporalClient.Close()

	worker, err := workflows.NewTemporalWorker(temporalClient, workflows.TemporalBootstrapTaskQueue, nil, workflows.WorkerOptions{EnableTenantSync: false})
	if err != nil {
		logger.Error().
			Err(err).
			Msg("temporal worker was not initialized")
		return
	}
	worker.RegisterZitadelSetupWorkflow()
	worker.RegisterTemporalSetupWorkflow()

	if err := worker.Run(); err != nil {
		logger.Error().
			Err(err).
			Msg("temporal worker failed to run")
		return
	}
}
