package main

import (
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/urfave/cli/v2"

	importgenapi "github.com/pyck-ai/pyck/backend/common/cmd/importgen/api"
	"github.com/pyck-ai/pyck/backend/common/cmd/importgen/internal"
	"github.com/pyck-ai/pyck/backend/common/cmd/importgen/types"
)

const (
	logPrefix         = "importgen: "
	defaultSchemaDir  = "./graph"
	defaultClientPath = "./api/internal/client_gen.go"
	defaultOutputDir  = "./api"
)

var (
	verbose bool
	dryRun  bool
)

func main() {
	log.SetFlags(0)
	log.SetPrefix(logPrefix)

	app := &cli.App{
		Name:  "importgen",
		Usage: "Generate import/export registry from @pyckImportable directives and API client",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "verbose",
				Aliases: []string{"v"},
				Usage:   "Print verbose output",
			},
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "Print what would be generated without writing files",
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

	// Auto-detect service name and module base from current directory.
	detected, err := importgenapi.DetectServiceInfo(c.Context)
	if err != nil {
		return fmt.Errorf("detect service info: %w", err)
	}

	logVerbosef("Service name: %s", detected.ServiceName)
	logVerbosef("Module base: %s", detected.ModuleBase)

	// Parse GraphQL schema to find @pyckImportable entities.
	entities, err := importgenapi.ParseImportableEntities(defaultSchemaDir)
	if err != nil {
		return fmt.Errorf("parse schema: %w", err)
	}

	if len(entities) == 0 {
		logVerbosef("No @pyckImportable entities found, skipping")
		return nil
	}

	logVerbosef("Found %d @pyckImportable entities", len(entities))

	// Check that client file exists.
	if _, err := os.Stat(defaultClientPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w %q", types.ErrClientFileNotFound, defaultClientPath)
	}

	// Parse client interface to build method index.
	methods, err := internal.ParseClientMethods(defaultClientPath)
	if err != nil {
		return fmt.Errorf("parse client: %w", err)
	}

	logVerbosef("Found %d client methods", len(methods))

	// Resolve each entity: match to client methods, detect accessor chains.
	var resolved []types.RegistryEntity
	for _, entry := range entities {
		entity, err := importgenapi.MatchEntity(entry, methods, defaultClientPath)
		if err != nil {
			return fmt.Errorf("entity %q: %w", entry.TypeName, err)
		}
		resolved = append(resolved, entity)
		logVerbosef("  %s: List=%s Create=%s Update=%s", entity.TypeName, entity.ListMethod, entity.CreateMethod, entity.UpdateMethod)
	}

	// Detect if model import is needed.
	var modelImportPath string
	for _, e := range resolved {
		if importgenapi.HasModelPrefix(e.CreateInputType) || importgenapi.HasModelPrefix(e.UpdateInputType) {
			modelImportPath = detected.ModuleBase + "/" + detected.ServiceName + "/model"
			break
		}
	}

	// Generate import_gen.go.
	tmplData := types.TemplateData{
		ServiceName:     detected.ServiceName,
		ModelImportPath: modelImportPath,
		Entities:        resolved,
	}

	if dryRun {
		log.Printf("[DRY-RUN] Would write %s/%s with %d entities", defaultOutputDir, "import_gen.go", len(resolved))
		return nil
	}

	if err := os.MkdirAll(defaultOutputDir, 0o755); err != nil {
		return err
	}

	if err := internal.WriteRegistryFile(tmplData, defaultOutputDir); err != nil {
		return fmt.Errorf("write registry: %w", err)
	}

	logVerbosef("Generated: %s/%s (%d entities)", defaultOutputDir, "import_gen.go", len(resolved))
	return nil
}

func logVerbosef(format string, args ...any) {
	if verbose {
		log.Printf(format, args...)
	}
}
