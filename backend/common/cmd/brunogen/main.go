package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v2"

	"github.com/pyck-ai/pyck/backend/common/cmd/brunogen/api"
	"github.com/pyck-ai/pyck/backend/common/cmd/brunogen/types"
)

const (
	defaultGraphQLFile = "./api/graph/apigen_gen.graphql"
	defaultOutputDir   = "../../.bruno/examples"
	defaultTestsDir    = "../../.bruno/tests"
	defaultBackendDir  = "../"
	defaultTestdataDir = "./api/testdata"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("brunogen: ")

	commonFlags := []cli.Flag{
		&cli.BoolFlag{
			Name:    "verbose",
			Aliases: []string{"v"},
			Usage:   "Print the names of files as they are generated",
		},
		&cli.BoolFlag{
			Name:  "dry-run",
			Usage: "Print what would be generated without writing files",
		},
	}

	app := &cli.App{
		Name:   "brunogen",
		Usage:  "Generate Bruno API client files from GraphQL schema files",
		Flags:  commonFlags,
		Action: runAll,
		Commands: []*cli.Command{
			{
				Name:  "examples",
				Usage: "Generate Bruno examples collection (one file per operation, per-service)",
				Flags: append(commonFlags,
					&cli.StringFlag{
						Name:    "graphql",
						Aliases: []string{"g"},
						Usage:   "Path to the apigen_gen.graphql file",
						Value:   defaultGraphQLFile,
					},
					&cli.StringFlag{
						Name:    "service",
						Aliases: []string{"s"},
						Usage:   "Service name (auto-detected from --graphql path if not specified)",
					},
					&cli.StringFlag{
						Name:    "output",
						Aliases: []string{"o"},
						Usage:   "Output directory for the examples Bruno collection",
						Value:   defaultOutputDir,
					},
				),
				Action: runExamples,
			},
			{
				Name:  "tests",
				Usage: "Generate Bruno tests collection (multi-step scenarios, cross-service)",
				Flags: append(commonFlags,
					&cli.StringFlag{
						Name:    "service",
						Aliases: []string{"s"},
						Usage:   "Service name (auto-detected from working directory if not specified)",
					},
					&cli.StringFlag{
						Name:  "testdata",
						Usage: "Path to the testdata directory",
						Value: defaultTestdataDir,
					},
					&cli.StringFlag{
						Name:  "backend-dir",
						Usage: "Path to the backend directory containing all service subdirectories",
						Value: defaultBackendDir,
					},
					&cli.StringFlag{
						Name:  "output-tests",
						Usage: "Output directory for the tests Bruno collection",
						Value: defaultTestsDir,
					},
				),
				Action: runTests,
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatalf("%v", err)
	}
}

func runAll(c *cli.Context) error {
	graphqlFile := defaultGraphQLFile
	if _, err := os.Stat(graphqlFile); errors.Is(err, os.ErrNotExist) {
		return types.ErrGraphQLFileNotExist
	}

	if err := api.GenerateExamples(types.Config{
		GraphQLFile: graphqlFile,
		OutputDir:   defaultOutputDir,
		Verbose:     c.Bool("verbose"),
		DryRun:      c.Bool("dry-run"),
	}); err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	serviceName, err := api.DetectServiceNameFromDir(cwd)
	if err != nil {
		return err
	}
	absBackend, err := filepath.Abs(defaultBackendDir)
	if err != nil {
		return err
	}

	return api.GenerateScenarios(types.Config{
		ServiceName:    serviceName,
		BackendDir:     absBackend,
		TestdataDir:    defaultTestdataDir,
		OutputTestsDir: defaultTestsDir,
		Verbose:        c.Bool("verbose"),
		DryRun:         c.Bool("dry-run"),
	})
}

func runExamples(c *cli.Context) error {
	graphqlFile := c.String("graphql")
	if _, err := os.Stat(graphqlFile); errors.Is(err, os.ErrNotExist) {
		return types.ErrGraphQLFileNotExist
	}

	cfg := types.Config{
		GraphQLFile: graphqlFile,
		ServiceName: c.String("service"),
		OutputDir:   c.String("output"),
		Verbose:     c.Bool("verbose"),
		DryRun:      c.Bool("dry-run"),
	}
	return api.GenerateExamples(cfg)
}

func runTests(c *cli.Context) error {
	serviceName := c.String("service")
	if serviceName == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		serviceName, err = api.DetectServiceNameFromDir(cwd)
		if err != nil {
			return err
		}
	}

	backendDir := c.String("backend-dir")
	absBackend, err := filepath.Abs(backendDir)
	if err != nil {
		return err
	}

	cfg := types.Config{
		ServiceName:    serviceName,
		BackendDir:     absBackend,
		TestdataDir:    c.String("testdata"),
		OutputTestsDir: c.String("output-tests"),
		Verbose:        c.Bool("verbose"),
		DryRun:         c.Bool("dry-run"),
	}
	return api.GenerateScenarios(cfg)
}
