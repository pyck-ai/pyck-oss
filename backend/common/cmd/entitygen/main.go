package main

import (
	_ "embed"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/urfave/cli/v2"
)

//go:embed templates/entities.go.tmpl
var entitiesTemplate string

const (
	logPrefix           = "entitygen: "
	outputFileName      = "entities_gen.go"
	defaultSchemaPath   = "../../*/ent/schema/*.go"
	defaultOutputPath   = "."
	dryRunPrefix        = "[DRY-RUN] Would write: "
	generatedPrefix     = "Generated: "
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
		Name:  "entitygen",
		Usage: "Generate entity type list from Ent schemas",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "schema",
				Aliases: []string{"s"},
				Usage:   "Glob pattern for ent schema files to scan",
				Value:   defaultSchemaPath,
			},
			&cli.StringFlag{
				Name:    "output",
				Aliases: []string{"o"},
				Usage:   "Output directory for generated file",
				Value:   defaultOutputPath,
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

	schemaPattern := c.String("schema")
	outputDir := c.String("output")

	if verbose {
		log.Printf("Schema files pattern: %s", schemaPattern)
		log.Printf("Output directory: %s", outputDir)
		if dryRun {
			log.Printf("Dry-run mode enabled - no files will be written")
		}
	}

	// Find all Ent schema files
	entities, err := extractEntities(schemaPattern)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrExtractEntities, err)
	}

	if verbose {
		log.Printf("Found %d entities", len(entities))
	}

	// Generate the output file
	if err := generateFile(entities, outputDir); err != nil {
		return fmt.Errorf("%w: %w", ErrGenerateFile, err)
	}

	if dryRun {
		logDryRunf("%s", outputFileName)
		logDryRunf("...")
		logDryRunf("func DataTypeEntities() []string {")
		logDryRunf("    return []string{")
		for _, entity := range entities {
			logDryRunf("        %q,", entity)
		}
		logDryRunf("    }")
		logDryRunf("}")
		logDryRunf("...")
	} else {
		logVerbosef(generatedPrefix+"%s with %d entities", outputFileName, len(entities))
	}

	return nil
}

// logVerbosef logs a message only if verbose mode is enabled
func logVerbosef(format string, args ...interface{}) {
	if verbose {
		log.Printf(format, args...)
	}
}

// logDryRunf logs a dry-run message regardless of verbose settings
func logDryRunf(format string, args ...interface{}) {
	log.Printf(dryRunPrefix+format, args...)
}

// extractEntities scans all Ent schema files and extracts entity names from entgql.Type() annotations
func extractEntities(schemaPattern string) ([]string, error) {
	entitySet := make(map[string]bool)

	if verbose {
		log.Printf("Scanning schema files with pattern: %s", schemaPattern)
	}

	// Use glob to find all matching schema files
	files, err := filepath.Glob(schemaPattern)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGlobSchemaFiles, err)
	}

	if verbose {
		log.Printf("Found %d schema files", len(files))
	}

	for _, file := range files {
		// Skip non-.go files
		if !strings.HasSuffix(file, ".go") {
			continue
		}

		typeName, err := extractTypeFromSchemaFile(file)
		if err != nil {
			return nil, fmt.Errorf("%w from %s: %w", ErrExtractType, file, err)
		}

		if typeName != "" {
			entitySet[typeName] = true
			if verbose {
				log.Printf("Extracted entity: %s from %s", typeName, file)
			}
		} else if verbose {
			log.Printf("Skipping file (no type declaration or DataMixin): %s", file)
		}
	}

	// Convert set to sorted slice
	entities := make([]string, 0, len(entitySet))
	for entity := range entitySet {
		entities = append(entities, entity)
	}
	sort.Strings(entities)

	return entities, nil
}

// extractTypeFromSchemaFile parses an Ent schema file and extracts the type name
// from the struct declaration, only if the entity uses DataMixin.
// Returns the type name converted to snake_case.
func extractTypeFromSchemaFile(filename string) (string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrParseFile, err)
	}

	var typeName string
	var hasDataMixin bool

	// Walk the AST to find the type declaration and Mixin() method
	ast.Inspect(file, func(n ast.Node) bool {
		// Look for type declarations
		genDecl, ok := n.(*ast.GenDecl)
		if ok && genDecl.Tok == token.TYPE {
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				// Check if it's a struct type
				if _, ok := typeSpec.Type.(*ast.StructType); ok {
					// This is the schema type name
					typeName = typeSpec.Name.Name
				}
			}
		}

		// Check for Mixin() method containing DataMixin
		funcDecl, ok := n.(*ast.FuncDecl)
		if ok && funcDecl.Name.Name == "Mixin" && funcDecl.Body != nil {
			hasDataMixin = checkForDataMixin(funcDecl.Body)
		}

		return true
	})

	// Only return the type if the entity uses DataMixin
	if !hasDataMixin {
		return "", nil
	}

	// Convert to snake_case before returning
	return toSnakeCase(typeName), nil
}

// toSnakeCase converts a PascalCase or camelCase string to snake_case
// Handles existing underscores without duplicating them
func toSnakeCase(s string) string {
	var result strings.Builder
	for i, r := range s {
		// Skip adding underscore if the previous character was already an underscore
		if i > 0 && r >= 'A' && r <= 'Z' {
			// Check if previous rune was not an underscore
			prevRune := rune(s[i-1])
			if prevRune != '_' {
				result.WriteRune('_')
			}
		}
		if r >= 'A' && r <= 'Z' {
			result.WriteRune(r + ('a' - 'A'))
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// checkForDataMixin walks the Mixin() method body to check if DataMixin is present
func checkForDataMixin(body *ast.BlockStmt) bool {
	found := false

	ast.Inspect(body, func(n ast.Node) bool {
		// Look for composite literals (struct initialization)
		compLit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}

		// Check if the type is mixin.DataMixin
		selExpr, ok := compLit.Type.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		ident, ok := selExpr.X.(*ast.Ident)
		if ok && ident.Name == "mixin" && selExpr.Sel.Name == "DataMixin" {
			found = true
			return false // Stop walking once we found it
		}

		return true
	})

	return found
}

// generateFile creates the entities_gen.go file using the template
func generateFile(entities []string, outputDir string) error {
	outputPath := filepath.Join(outputDir, outputFileName)
	if verbose {
		log.Printf("Generating output file: %s", outputPath)
	}

	tmpl, err := template.New("entities").Parse(entitiesTemplate)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrParseTemplate, err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, map[string]interface{}{
		"Entities": entities,
	}); err != nil {
		return fmt.Errorf("%w: %w", ErrExecuteTemplate, err)
	}

	// Format the generated code
	formatted, err := format.Source([]byte(buf.String()))
	if err != nil {
		// If formatting fails in dry-run, just log it
		if dryRun {
			if verbose {
				log.Printf("Warning: generated code would have formatting issues")
			}
			return nil
		}
		// In real mode, write unformatted code for debugging
		if verbose {
			log.Printf("Warning: formatting failed, writing unformatted code")
		}
		return os.WriteFile(outputPath, []byte(buf.String()), 0o600)
	}

	// In dry-run mode, don't write the file
	if dryRun {
		return nil
	}

	return os.WriteFile(outputPath, formatted, 0o600)
}
