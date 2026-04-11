package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"entgo.io/contrib/entgql"
	"entgo.io/ent/entc"
	ent "entgo.io/ent/entc/gen"
	"github.com/goccy/go-yaml"
	"github.com/urfave/cli/v2"
)

var (
	ErrSchemaPathNotExist = errors.New("required path does not exist")
	ErrConfigFileNotExist = errors.New("config file does not exist")
)

type Config struct {
	EntTemplatePaths []string
	EntTargetPath    string
	EntSchemaPath    string
	GqlConfigPath    string
	GqlSchemaPath    string
	Verbose          bool
	Force            bool
	DryRun           bool
}

var defaultConfig = Config{
	EntTemplatePaths: []string{"./ent/template", "../common/cmd/entc/templates/ent"},
	EntTargetPath:    "./ent/gen",
	EntSchemaPath:    "./ent/schema",
	GqlConfigPath:    "./gqlgen.yml",
	GqlSchemaPath:    "./graph/ent.graphql",
	Verbose:          false,
	Force:            false,
	DryRun:           false,
}

type generator struct {
	packageName string
	config      Config
	entOpts     []entc.Option
}

func (gen *generator) registerGraphQLExtension() error {
	if _, err := os.Stat(gen.config.GqlConfigPath); err != nil {
		return nil //nolint:nilerr
	}

	if gen.config.Verbose {
		log.Printf("Registering GraphQL extension with config %s and schema %s", gen.config.GqlConfigPath, gen.config.GqlSchemaPath)
	}

	if gen.config.DryRun {
		return nil
	}

	gqlOpts := []entgql.ExtensionOption{
		// WithTemplates must come before WithWhereInputs so the where template is appended.
		entgql.WithTemplates(entgql.AllTemplates...),
		entgql.WithWhereInputs(true),
		entgql.WithSchemaGenerator(),
		entgql.WithSchemaHook(jsonbOrderSchemaHook),
		entgql.WithConfigPath(gen.config.GqlConfigPath),
		entgql.WithSchemaPath(gen.config.GqlSchemaPath),
		entgql.WithNodeDescriptor(false),
		entgql.WithRelaySpec(true),
	}

	ext, err := entgql.NewExtension(gqlOpts...)
	if err != nil {
		return fmt.Errorf("failed to create GraphQL extension: %w", err)
	}

	gen.entOpts = append(gen.entOpts, entc.Extensions(ext, &jsonbExtension{}))

	return nil
}

func (gen *generator) registerCustomTemplates() {
	for _, dir := range gen.config.EntTemplatePaths {
		if _, err := os.Stat(dir); err == nil {
			if gen.config.Verbose {
				log.Printf("Using custom template directory: %s", dir)
			}
			if !gen.config.DryRun {
				gen.entOpts = append(gen.entOpts, entc.TemplateDir(dir))
			}
		}
	}
}

func (gen *generator) generateStage1() error {
	if gen.config.Verbose {
		log.Printf(`Generating stage 1:
  Package: %s
  Target: %s
  Schema: %s
  Features: none`, gen.packageName, gen.config.EntTargetPath, gen.config.EntSchemaPath)
	}

	if gen.config.DryRun {
		return nil
	}

	config := &ent.Config{
		Package:  gen.packageName,
		Target:   gen.config.EntTargetPath,
		Features: []ent.Feature{},
	}

	opts := append(gen.entOpts, entc.BuildTags("skiphooks", "skippolicy"))

	return entc.Generate(gen.config.EntSchemaPath, config, opts...)
}

func (gen *generator) generateStage2() error {
	if gen.config.Verbose {
		log.Printf(`Generating stage 2:
  Package: %s
  Target: %s
  Schema: %s
  Features: Privacy`, gen.packageName, gen.config.EntTargetPath, gen.config.EntSchemaPath)
	}

	if gen.config.DryRun {
		return nil
	}

	config := &ent.Config{
		Package: gen.packageName,
		Target:  gen.config.EntTargetPath,
		Features: []ent.Feature{
			ent.FeaturePrivacy,
		},
	}

	opts := append(gen.entOpts, entc.BuildTags("skippolicy"))

	return entc.Generate(gen.config.EntSchemaPath, config, opts...)
}

func (gen *generator) generateStage3() error {
	if gen.config.Verbose {
		log.Printf(`Generating stage 3:
  Package: %s
  Target: %s
  Schema: %s
  Features: EntQL, Privacy, Intercept, Upsert, ExecQuery, VersionedMigration`, gen.packageName, gen.config.EntTargetPath, gen.config.EntSchemaPath)
	}

	if gen.config.DryRun {
		return nil
	}

	config := &ent.Config{
		Package: gen.packageName,
		Target:  gen.config.EntTargetPath,
		Features: []ent.Feature{
			ent.FeatureEntQL,
			ent.FeaturePrivacy,
			ent.FeatureIntercept,
			ent.FeatureUpsert,
			ent.FeatureExecQuery,
			ent.FeatureVersionedMigration,
		},
	}

	return entc.Generate(gen.config.EntSchemaPath, config, gen.entOpts...)
}

func Generate(config Config, pkgname string) error {
	// Validate required paths
	if _, err := os.Stat(config.EntSchemaPath); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrSchemaPathNotExist, config.EntSchemaPath)
	}

	gen := generator{
		packageName: pkgname + "/ent/gen",
		config:      config,
		entOpts:     []entc.Option{},
	}

	if err := gen.registerGraphQLExtension(); err != nil {
		return fmt.Errorf("failed registering GraphQL extension: %w", err)
	}

	gen.registerCustomTemplates()

	allStages := []func() error{
		gen.generateStage1,
		gen.generateStage2,
		gen.generateStage3,
	}

	if config.DryRun {
		if config.Verbose {
			log.Printf("Dry run: validation passed for package %s", pkgname)
		}
	}

	// Try directly running the last stage first, if it succeeds, we are done.
	if !config.Force {
		if err := tryLastStageOnly(allStages, config.Verbose); err == nil {
			return nil
		}
	}

	// Run all stages from start to end
	for i, stage := range allStages {
		if config.Verbose {
			log.Printf("Running stage %d...", i+1)
		}
		if err := stage(); err != nil {
			return fmt.Errorf("failed running stage %d: %w", i+1, err)
		}
	}

	return nil
}

func tryLastStageOnly(allStages []func() error, verbose bool) error {
	lastStage := allStages[len(allStages)-1]
	if verbose {
		log.Printf("Trying to directly run last stage...")
	}

	if err := lastStage(); err != nil {
		if verbose {
			log.Printf("Last stage failed. Falling back to full generation: %v", err)
		}
		return err
	}

	if verbose {
		log.Printf("Last stage succeeded, skipping remaining stages")
	}
	return nil
}

func main() {
	app := &cli.App{
		Name:  "codegen",
		Usage: "Generate Ent code with GraphQL extensions",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Usage: "Path to YAML config file (default: entc.yml if exists)",
			},
			&cli.StringFlag{
				Name:  "ent-target",
				Value: defaultConfig.EntTargetPath,
				Usage: "Path to the Ent target directory",
			},
			&cli.StringFlag{
				Name:  "ent-schema",
				Value: defaultConfig.EntSchemaPath,
				Usage: "Path to the Ent schema directory",
			},
			&cli.StringFlag{
				Name:  "gql-config",
				Value: defaultConfig.GqlConfigPath,
				Usage: "Path to the gqlgen config file",
			},
			&cli.StringFlag{
				Name:  "gql-schema",
				Value: defaultConfig.GqlSchemaPath,
				Usage: "Path to the GraphQL schema file",
			},
			&cli.StringSliceFlag{
				Name:  "template-dir",
				Value: cli.NewStringSlice(defaultConfig.EntTemplatePaths...),
				Usage: "Paths to custom template directories",
			},
			&cli.BoolFlag{
				Name:    "verbose",
				Aliases: []string{"v"},
				Value:   defaultConfig.Verbose,
				Usage:   "Enable verbose logging",
			},
			&cli.BoolFlag{
				Name:    "force",
				Aliases: []string{"f"},
				Value:   defaultConfig.Force,
				Usage:   "Force full regeneration without stage skipping",
			},
			&cli.BoolFlag{
				Name:  "dry-run",
				Value: defaultConfig.DryRun,
				Usage: "Validate configuration without generating code",
			},
		},
		Action: func(c *cli.Context) error {
			config := defaultConfig

			// Determine config file
			var configFile string
			if c.IsSet("config") {
				configFile = c.String("config")
				if _, err := os.Stat(configFile); os.IsNotExist(err) {
					return fmt.Errorf("%w: %s", ErrConfigFileNotExist, configFile)
				}
			} else {
				if _, err := os.Stat("entc.yml"); err == nil {
					configFile = "entc.yml"
				}
			}

			// Load YAML config if exists
			if _, err := os.Stat(configFile); err == nil {
				data, err := os.ReadFile(configFile)
				if err != nil {
					return fmt.Errorf("failed to read config file %s: %w", configFile, err)
				}
				if err := yaml.Unmarshal(data, &config); err != nil {
					return fmt.Errorf("failed to parse config file %s: %w", configFile, err)
				}
			}

			// Override with CLI flags
			config.EntTemplatePaths = c.StringSlice("template-dir")
			config.EntTargetPath = c.String("ent-target")
			config.EntSchemaPath = c.String("ent-schema")
			config.GqlConfigPath = c.String("gql-config")
			config.GqlSchemaPath = c.String("gql-schema")
			config.Verbose = c.Bool("verbose")
			config.Force = c.Bool("force")
			config.DryRun = c.Bool("dry-run")

			// Configure logger
			log.SetFlags(0) // Drop date/time
			if config.DryRun {
				log.SetPrefix("[DRY RUN] ")
			} else {
				log.SetPrefix("")
			}

			// Get the module name using go list .
			cmd := exec.CommandContext(c.Context, "go", "list", ".")
			var out bytes.Buffer
			cmd.Stdout = &out
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("failed to run 'go list .': %w", err)
			}
			pkgname := strings.TrimSpace(out.String())

			if err := Generate(config, pkgname); err != nil {
				return fmt.Errorf("generation failed: %w", err)
			}

			return nil
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatalf("Error: %v", err)
	}
}
