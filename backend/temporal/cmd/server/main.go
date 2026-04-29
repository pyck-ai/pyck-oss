package main

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v2"
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
	_ = app.RunContext(ctx, os.Args)
}

func buildCLI() *cli.App {
	bi := env.GetBuildInfo()

	app := cli.NewApp()
	app.Name = serviceName
	app.Usage = "Temporal server"
	app.Version = fmt.Sprintf("%s-pyck-%s", temporalheaders.ServerVersion, bi.GitCommitSHA())

	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:    "root",
			Aliases: []string{"r"},
			Value:   ".",
			Usage:   "root directory of execution environment",
			EnvVars: []string{temporalconfig.EnvKeyRoot},
		},
		&cli.StringFlag{
			Name:    "config",
			Aliases: []string{"c"},
			Value:   "config",
			Usage:   "config dir path relative to root",
			EnvVars: []string{temporalconfig.EnvKeyConfigDir},
		},
		&cli.StringFlag{
			Name:    "env",
			Aliases: []string{"e"},
			Value:   "development",
			Usage:   "runtime environment",
			EnvVars: []string{temporalconfig.EnvKeyEnvironment},
		},
		&cli.StringFlag{
			Name:    "zone",
			Aliases: []string{"az"},
			Usage:   "availability zone",
			EnvVars: []string{temporalconfig.EnvKeyAvailabilityZone},
		},
		&cli.BoolFlag{
			Name:    "allow-no-auth",
			Usage:   "allow no authorizer",
			EnvVars: []string{temporalconfig.EnvKeyAllowNoAuth},
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
					Value:   cli.NewStringSlice(temporal.DefaultServices...),
					Usage:   "service(s) to start",
				},
			},
			Before: func(c *cli.Context) error {
				if c.Args().Len() > 0 {
					return cli.Exit("ERROR: start command doesn't support arguments. Use --service flag instead.", 1)
				}
				return nil
			},
			Action: func(c *cli.Context) error {
				if err := runServer(c); err != nil {
					return cli.Exit(fmt.Errorf("server exited unexpectedly: %w", err), 1)
				}
				return nil
			},
		},
		{
			Name:      "render-config",
			Usage:     "Render server config template",
			ArgsUsage: " ",
			Action: func(c *cli.Context) error {
				cfg, err := temporalconfig.LoadConfig(
					c.String("env"),
					c.String("config"),
					c.String("zone"),
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
