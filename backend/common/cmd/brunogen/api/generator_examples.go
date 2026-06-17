package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gen "github.com/pyck-ai/pyck/backend/common/cmd/brunogen/internal"
	"github.com/pyck-ai/pyck/backend/common/cmd/brunogen/types"
)

// repoRelPath returns absPath relative to the nearest ancestor directory that
// contains a go.work file (the repository root). Falls back to absPath if the
// root cannot be determined.
func repoRelPath(absPath string) string {
	dir := filepath.Dir(absPath)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			if rel, err := filepath.Rel(dir, absPath); err == nil {
				return rel
			}
			return absPath
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return absPath
		}
		dir = parent
	}
}

// crudSeq returns a Bruno seq number for an operation based on its CRUD role:
// Create=1, Get/Read=2, Update/Execute=3, Delete=4.
// This ensures requests within each resource folder are sorted in CRUD order.
func crudSeq(opName string) int {
	lower := strings.ToLower(opName)
	switch {
	case strings.HasPrefix(lower, "create"):
		return 1
	case strings.HasPrefix(lower, "get"):
		return 2
	case strings.HasPrefix(lower, "update"), strings.HasPrefix(lower, "execute"), strings.HasPrefix(lower, "set"):
		return 3
	case strings.HasPrefix(lower, "delete"), strings.HasPrefix(lower, "remove"):
		return 4
	default:
		return 5
	}
}

// GenerateExamples generates the Bruno examples collection.
// For each GraphQL operation in cfg.GraphQLFile, one .yml file is written to
// cfg.OutputDir.  If a matching .example.yaml fixture exists, the file is
// enriched with pre-filled variables and assertions.
func GenerateExamples(cfg types.Config) error {
	ops, cfg, err := prepare(cfg)
	if err != nil {
		return err
	}

	serviceDir := filepath.Join(cfg.OutputDir, cfg.ServiceName)

	exampleIndex, err := BuildExampleIndex(cfg.TestdataDir)
	if err != nil {
		return fmt.Errorf("failed to build example index: %w", err)
	}

	for _, op := range append(ops.Queries, ops.Mutations...) {
		if err := generateExampleFile(serviceDir, cfg, op, exampleIndex); err != nil {
			return fmt.Errorf("failed to generate example for %s: %w", op.Name, err)
		}
	}
	return nil
}

func generateExampleFile(serviceDir string, cfg types.Config, op types.Operation, exampleIndex map[string]string) error {
	resourceName := op.ReturnType
	if resourceName == "" {
		resourceName = gen.ExtractResourceName(op.Name)
	}
	resourceName = gen.Singularize(gen.StripServicePrefix(resourceName, cfg.ServiceName))
	resourceDir := filepath.Join(serviceDir, resourceName)

	if !cfg.DryRun {
		if err := os.MkdirAll(resourceDir, 0o755); err != nil {
			return fmt.Errorf("failed to create resource directory: %w", err)
		}
		if err := gen.WriteFolderYML(serviceDir, cfg.ServiceName, "", cfg); err != nil {
			return err
		}
		if err := gen.WriteFolderYML(resourceDir, resourceName, "", cfg); err != nil {
			return err
		}
	}

	seq := crudSeq(op.Name)
	filename := filepath.Join(resourceDir, fmt.Sprintf("%02d_%s_gen.yml", seq, strings.ToLower(op.Name)))

	if dataFile := exampleIndex[strings.ToLower(op.Name)]; dataFile != "" {
		scenario, err := LoadExampleScenario(dataFile)
		if err != nil {
			return fmt.Errorf("failed to load example scenario from %s: %w", dataFile, err)
		}
		absDataFile, _ := filepath.Abs(dataFile)
		return generateExampleFileFromScenario(filename, cfg, op, scenario, repoRelPath(absDataFile))
	}

	content, err := gen.RenderFile(gen.ExampleTemplate, cfg, op, "", "", nil, nil, nil, false, false, crudSeq(op.Name), "bru-example", nil, "", 0, nil)
	if err != nil {
		return fmt.Errorf("failed to render example file: %w", err)
	}
	return gen.WriteFile(filename, content, cfg)
}

func generateExampleFileFromScenario(filename string, cfg types.Config, op types.Operation, scenario *types.ExampleScenario, sourceFile string) error {
	tests, useBody, useReqVars := gen.ProcessExampleScenario(*scenario)
	skipChecks := gen.ResolveSkip(scenario.Skip, "bru-example")
	content, err := gen.RenderFile(gen.ExampleTemplate, cfg, op, scenario.Name, scenario.Description, scenario.Vars, tests, nil, useBody, useReqVars, crudSeq(op.Name), "bru-example", skipChecks, sourceFile, 0, nil)
	if err != nil {
		return fmt.Errorf("failed to render example for %q: %w", scenario.Name, err)
	}
	return gen.WriteFile(filename, content, cfg)
}
