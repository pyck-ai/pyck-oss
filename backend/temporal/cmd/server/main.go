package main

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"
	temporalconfig "go.temporal.io/server/common/config"
	temporalheaders "go.temporal.io/server/common/headers"
	"go.temporal.io/server/temporal"

	_ "go.temporal.io/server/common/persistence/sql/sqlplugin/mysql"      // needed to load mysql plugin
	_ "go.temporal.io/server/common/persistence/sql/sqlplugin/postgresql" // needed to load postgresql plugin
	_ "go.temporal.io/server/common/persistence/sql/sqlplugin/sqlite"     // needed to load sqlite plugin

	"github.com/pyck-ai/pyck/backend/common/env"
	pyckconfig "github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/log"
)

const (
	serviceName = "temporal"
)

func main() {
	ctx, _ := log.SetupLogger(context.Background(), serviceName, pyckconfig.LogConfig{})

	app := buildCLI()
	if err := app.Run(ctx, os.Args); err != nil {
		// cli.Exit errors terminate inside Run; only unexpected errors reach here.
		log.ForContext(ctx).Fatal().Err(err).Msg("temporal server exited with error")
	}
}

func buildCLI() *cli.Command {
	bi := env.GetBuildInfo()

	app := &cli.Command{
		Name:    serviceName,
		Usage:   "Temporal server",
		Version: fmt.Sprintf("%s-pyck-%s", temporalheaders.ServerVersion, bi.GitCommitSHA()),
	}

	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:    "root",
			Aliases: []string{"r"},
			Value:   ".",
			Usage:   "root directory of execution environment",
			Sources: cli.EnvVars(temporalconfig.EnvKeyRoot),
		},
		&cli.StringFlag{
			Name:    "config",
			Aliases: []string{"c"},
			Value:   "config",
			Usage:   "config dir path relative to root",
			Sources: cli.EnvVars(temporalconfig.EnvKeyConfigDir),
		},
		&cli.StringFlag{
			Name:    "env",
			Aliases: []string{"e"},
			Value:   "development",
			Usage:   "runtime environment",
			Sources: cli.EnvVars(temporalconfig.EnvKeyEnvironment),
		},
		&cli.StringFlag{
			Name:    "zone",
			Aliases: []string{"az"},
			Usage:   "availability zone",
			Sources: cli.EnvVars(temporalconfig.EnvKeyAvailabilityZone),
		},
		&cli.StringFlag{
			Name:    "config-file",
			Usage:   "path to config file (absolute or relative to current working directory)",
			Sources: cli.EnvVars(temporalconfig.EnvKeyConfigFile),
		},
		&cli.BoolFlag{
			Name:    "allow-no-auth",
			Usage:   "allow no authorizer",
			Sources: cli.EnvVars(temporalconfig.EnvKeyAllowNoAuth),
		},
	}

	app.Commands = []*cli.Command{
		{
			Name:      "start",
			Usage:     "Start Temporal server",
			ArgsUsage: " ",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:    "services",
					Aliases: []string{"s"},
					Usage:   "comma separated list of services to start. Deprecated",
					Hidden:  true,
				},
				&cli.StringSliceFlag{
					Name:    "service",
					Aliases: []string{"svc"},
					Value:   temporal.DefaultServices,
					Usage:   "service(s) to start",
				},
			},
			Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
				if cmd.Args().Len() > 0 {
					return ctx, cli.Exit("ERROR: start command doesn't support arguments. Use --service flag instead.", 1)
				}
				return ctx, nil
			},
			Action: func(ctx context.Context, cmd *cli.Command) error {
				if err := runServer(ctx, cmd); err != nil {
					return cli.Exit(fmt.Errorf("server exited unexpectedly: %w", err), 1)
				}
				return nil
			},
		},
		{
			Name:      "health",
			Usage:     "Check Temporal frontend health",
			ArgsUsage: " ",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:    "address",
					Aliases: []string{"a"},
					Value:   "127.0.0.1:7236",
					Usage:   "address of the Temporal frontend to check",
					Sources: cli.EnvVars("TEMPORAL_ADDRESS"),
				},
			},
			Action: checkHealth,
		},
		{
			Name:      "render-config",
			Usage:     "Render server config template",
			ArgsUsage: " ",
			Action: func(ctx context.Context, cmd *cli.Command) error {
				cfg, err := temporalconfig.Load(
					temporalconfig.WithEnv(cmd.String("env")),
					temporalconfig.WithConfigDir(cmd.String("config")),
					temporalconfig.WithZone(cmd.String("zone")),
				)
				if err != nil {
					return cli.Exit(fmt.Errorf("unable to load configuration: %w", err), 1)
				}
				fmt.Println(cfg.String())
				return nil
			},
		},
	}

	return app
}
