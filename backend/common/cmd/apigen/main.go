package main

import (
	"fmt"
	"log"
	"os"

	"github.com/urfave/cli/v2"
)

const (
	logPrefix        = "apigen: "
	defaultSchemaDir = "./graph"
	defaultOutputDir = "./api/graph"
	generatedPrefix  = "Generated: "
	dryRunPrefix     = "[DRY-RUN] Would write: "
)

var (
	verbose bool
	dryRun  bool
)

func main() {
	// Configure log package to output without timestamps and with prefix
	log.SetFlags(0)
	log.SetPrefix(logPrefix)

	app := &cli.App{
		Name:  "apigen",
		Usage: "Generate GraphQL client query files from server-side schema definitions",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "schema",
				Aliases: []string{"s"},
				Usage:   "Directory containing server GraphQL schema files",
				Value:   defaultSchemaDir,
			},
			&cli.StringFlag{
				Name:    "output",
				Aliases: []string{"o"},
				Usage:   "Output directory for generated client query files",
				Value:   defaultOutputDir,
			},
			&cli.BoolFlag{
				Name:    "verbose",
				Aliases: []string{"v"},
				Usage:   "Print the names of files as they are processed",
				Value:   false,
			},
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "Print commands that would be executed without modifying files",
				Value: false,
			},
		},
		Action: run,
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatalf("%v", err)
	}
}

func run(c *cli.Context) error {
	verbose = c.Bool("verbose")
	dryRun = c.Bool("dry-run")

	schemaDir := c.String("schema")
	outputDir := c.String("output")

	if verbose {
		log.Printf("Schema directory: %s", schemaDir)
		log.Printf("Output directory: %s", outputDir)
		if dryRun {
			log.Printf("Dry-run mode enabled - no files will be written")
		}
	}

	// Verify schema directory exists
	if _, err := os.Stat(schemaDir); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrSchemaDirNotExist, schemaDir)
	}

	// Create output directory if it doesn't exist
	if !dryRun {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			return fmt.Errorf("failed to create output directory: %w", err)
		}
	}

	// Load and parse GraphQL schema files
	schema, err := loadSchema(schemaDir)
	if err != nil {
		return fmt.Errorf("failed to load schema: %w", err)
	}
	if verbose {
		log.Printf("Loaded schema with %d types", len(schema.Types))
	}

	// Generate client queries and mutations
	if err := generateClientQueries(schema, outputDir); err != nil {
		return fmt.Errorf("failed to generate client queries: %w", err)
	}

	return nil
}

func logVerbosef(format string, args ...interface{}) {
	if verbose {
		log.Printf(format, args...)
	}
}
