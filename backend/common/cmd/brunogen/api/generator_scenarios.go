package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gen "github.com/pyck-ai/pyck/backend/common/cmd/brunogen/internal"
	"github.com/pyck-ai/pyck/backend/common/cmd/brunogen/types"
)

// serviceOpsCache caches parsed operations per service name within a single run.
type serviceOpsCache map[string]*types.Operations

// load returns cached operations for the named service, loading from disk on
// the first access. backendDir must be the absolute path to the backend directory.
func (c serviceOpsCache) load(backendDir, service string) (*types.Operations, error) {
	if ops, ok := c[service]; ok {
		return ops, nil
	}
	schemaPath := filepath.Join(backendDir, service, "api", "graph", "apigen_gen.graphql")
	ops, err := ParseGraphQL(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load schema for service %q: %w", service, err)
	}

	// Merge any hand-written operations (apigen's operations_gen.graphql). These
	// cover federated cross-service relations that apigen cannot auto-generate
	// and run through the gateway. The file is optional; a missing one — or one
	// holding no operations — simply contributes nothing.
	handWrittenPath := filepath.Join(backendDir, service, "api", "operations_gen.graphql")
	if _, statErr := os.Stat(handWrittenPath); statErr == nil {
		extra, err := ParseGraphQL(handWrittenPath)
		switch {
		case errors.Is(err, types.ErrNoOperations):
			// Nothing to merge.
		case err != nil:
			return nil, fmt.Errorf("failed to load hand-written operations for service %q: %w", service, err)
		default:
			ops.Queries = append(ops.Queries, extra.Queries...)
			ops.Mutations = append(ops.Mutations, extra.Mutations...)
		}
	}

	c[service] = ops
	return ops, nil
}

// lookupQualifiedOp resolves a fully-qualified "service.operationName" string
// against the appropriate service schema. Returns ErrUnqualifiedOperation if
// the operation name contains no service prefix.
func (c serviceOpsCache) lookupQualifiedOp(backendDir, qualifiedOp string) (types.Operation, error) {
	service, opName := ParseQualifiedOperation(qualifiedOp)
	if service == "" {
		return types.Operation{}, fmt.Errorf("%w: %q", types.ErrUnqualifiedOperation, qualifiedOp)
	}
	ops, err := c.load(backendDir, service)
	if err != nil {
		return types.Operation{}, err
	}
	op, ok := ops.Find(opName)
	if !ok {
		return types.Operation{}, fmt.Errorf("operation %q: %w (service: %s)", opName, types.ErrOperationNotFound, service)
	}
	return op, nil
}

// GenerateScenarios generates the Bruno tests collection.
// For each .test.yaml file in cfg.TestdataDir/tests/, a subdirectory is
// created under cfg.OutputTestsDir containing one .yml file per step.
//
// When cfg.BackendDir is set (tests subcommand), operations must be fully
// qualified as "service.operationName" and are resolved against per-service
// schema files under BackendDir. When BackendDir is empty (legacy / direct
// API usage), operations are looked up in the single parsed GraphQL file.
func GenerateScenarios(cfg types.Config) error {
	var (
		ops *types.Operations
		err error
	)

	if cfg.BackendDir != "" {
		if cfg.ServiceName == "" {
			return types.ErrServiceNameRequired
		}
	} else {
		ops, cfg, err = prepare(cfg)
		if err != nil {
			return err
		}
	}

	testFiles, err := FindAllTestFiles(cfg.TestdataDir)
	if err != nil {
		return fmt.Errorf("failed to list test files: %w", err)
	}

	cache := make(serviceOpsCache)

	for _, testFile := range testFiles {
		scenario, err := LoadTestScenario(testFile)
		if err != nil {
			return fmt.Errorf("failed to load test scenario from %s: %w", testFile, err)
		}
		basename := scenarioBasename(testFile)
		absTestFile, _ := filepath.Abs(testFile)
		if err := generateScenarioDir(cfg, basename, scenario, ops, cache, repoRelPath(absTestFile)); err != nil {
			return fmt.Errorf("failed to generate scenario %q: %w", scenario.Name, err)
		}
	}
	return nil
}

// scenarioBasename returns the filename without directory and test-file suffixes.
// e.g. ".../tests/createfile.test.yaml" → "createfile".
func scenarioBasename(testFile string) string {
	base := filepath.Base(testFile)
	base = strings.TrimSuffix(base, ".test.yaml")
	base = strings.TrimSuffix(base, ".test.yml")
	return base
}

func generateScenarioDir(cfg types.Config, basename string, scenario *types.TestScenario, ops *types.Operations, cache serviceOpsCache, sourceFile string) error {
	serviceDir := filepath.Join(cfg.OutputTestsDir, cfg.ServiceName)
	scenarioDir := filepath.Join(serviceDir, gen.Slugify(scenario.Name))

	if !cfg.DryRun {
		if err := os.MkdirAll(scenarioDir, 0o755); err != nil {
			return fmt.Errorf("failed to create scenario directory: %w", err)
		}
		if err := gen.RemoveGeneratedFiles(scenarioDir); err != nil {
			return fmt.Errorf("failed to remove stale generated files: %w", err)
		}
		if err := gen.WriteFolderYML(serviceDir, cfg.ServiceName, "", cfg); err != nil {
			return err
		}
		if err := gen.WriteFolderYML(scenarioDir, scenario.Name, scenario.Description, cfg); err != nil {
			return err
		}
	}

	varNS := "bru-test_" + basename
	extractMap := gen.CollectExtracts(scenario.Steps, varNS)

	for i, step := range scenario.Steps {
		if step.Operation == "" {
			return fmt.Errorf("step %q in scenario %q: %w", step.Name, scenario.Name, types.ErrOperationRequired)
		}

		var stepOp types.Operation
		stepCfg := cfg
		if cfg.BackendDir != "" {
			// tests subcommand: resolve via fully-qualified "service.operationName"
			op, err := cache.lookupQualifiedOp(cfg.BackendDir, step.Operation)
			if err != nil {
				return fmt.Errorf("step %q in scenario %q: %w", step.Name, scenario.Name, err)
			}
			stepOp = op
			// Use the operation's own service name for URL generation so that
			// cross-service steps (e.g. management.createDataType inside an
			// inventory scenario) point to the correct endpoint.
			if stepService, _ := ParseQualifiedOperation(step.Operation); stepService != "" {
				stepCfg.ServiceName = stepService
			}
		} else {
			// legacy / direct API: look up in the single parsed operations set
			op, ok := ops.Find(step.Operation)
			if !ok {
				return fmt.Errorf("step %q in scenario %q: operation %q: %w", step.Name, scenario.Name, step.Operation, types.ErrOperationNotFound)
			}
			stepOp = op
		}

		seq := i + 1
		tests, useBody, useReqVars := gen.ProcessStep(step, varNS)
		extracts := extractMap[step.ID]
		skipChecks := gen.ResolveSkip(step.Skip, varNS)

		// apigen skips reverse-Connection fields in default queries to keep
		// responses bounded; augment per-step from the assertion refs so
		// nothing the test reads is silently undefined.
		stepOp.Content = gen.AugmentOperationContent(stepOp.Content, step.Tests)

		filename := filepath.Join(scenarioDir, fmt.Sprintf("%02d_%s_gen.yml", seq, strings.ToLower(stepOp.Name)))
		content, err := gen.RenderFile(gen.TestTemplate, stepCfg, stepOp, step.Name, step.Description, step.Vars, tests, extracts, useBody, useReqVars, seq, varNS, skipChecks, sourceFile, step.WaitAfterMs, step.Headers)
		if err != nil {
			return fmt.Errorf("failed to render step %q: %w", step.Name, err)
		}
		if err := gen.WriteFile(filename, content, cfg); err != nil {
			return err
		}
	}
	return nil
}
